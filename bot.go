package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/go-github/v47/github"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/urlfetch"
	"google.golang.org/appengine/user"
)

type GitHubToken struct {
	Token  string
	Secret string
}

var githubToken GitHubToken

const updateTokenForm = `
<html>
<body>
<form action="/update_github_token" method="post">
<label for="token">Token:</label>
<input type="text" name="token" id="token" value="%s">

<label for="secret">Secret:</label>
<input type="text" name="secret" id="secret" value="%s">

<input type="submit" value="Update token">
</form>
</body>
</html>
`

var (
	enhancementRegexp = regexp.MustCompile(`\[\s*x\s*\]\s*feature\s*request`)

	enhancementRegexpTitle = regexp.MustCompile("feature.?request|enhancement")

	newConfigurationRegexp = regexp.MustCompile(`\[\s*x\s*\]\s*this\s*feature\s*requires\s*new\s*configuration`)

	bugRegexp           = regexp.MustCompile(`\[\s*x\s*\]\s*bug`)
	documentationRegexp = regexp.MustCompile(`\[\s*x\s*\]\s*documentation\s*request`)
)

func main() {
	http.HandleFunc("/issues", issuesHandler)
	http.HandleFunc("/issue_comment", issueCommentHandler)
	http.HandleFunc("/update_github_token", updateTokenHandler)
	http.HandleFunc("/", logHandler)
	http.HandleFunc("/logs/", logsHandler)
	appengine.Main()
}

func updateTokenHandler(w http.ResponseWriter, r *http.Request) {
	ctx := appengine.NewContext(r)
	u := user.Current(ctx)
	if u == nil {
		url, err := user.LoginURL(ctx, "/update_github_token")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
		return
	}

	if u.String() != "michael@i3wm.org" {
		http.Error(w, "Unauthorized", http.StatusForbidden)
		return
	}

	if err := getGitHubToken(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Method == "POST" {
		k := datastore.NewKey(ctx, "GitHubToken", "githubtoken", 0, nil)
		t := GitHubToken{
			Token:  r.FormValue("token"),
			Secret: r.FormValue("secret"),
		}
		if _, err := datastore.Put(ctx, k, &t); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		githubToken = t
	}
	fmt.Fprintf(w, updateTokenForm, githubToken.Token, githubToken.Secret)
}

func getGitHubToken(ctx context.Context) error {
	if githubToken.Secret != "" && githubToken.Token != "" {
		return nil
	}
	k := datastore.NewKey(ctx, "GitHubToken", "githubtoken", 0, nil)
	return datastore.Get(ctx, k, &githubToken)
}

type githubTransport urlfetch.Transport

func (g *githubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", "i3-github-bot (run by github.com/stapelberg)")
	req.SetBasicAuth(githubToken.Token, "x-oauth-basic")
	res, err := (*urlfetch.Transport)(g).RoundTrip(req)
	return res, err
}

func discardResponse(resp *github.Response) {
	ioutil.ReadAll(resp.Body)
	resp.Body.Close()
}

// readAndVerifyBody verifies the HMAC signature to make sure this request was
// sent by GitHub with the configured secret key.
func readAndVerifyBody(r *http.Request) ([]byte, string, error) {
	ctx := appengine.NewContext(r)

	event := r.Header.Get("X-GitHub-Event")
	if event == "" {
		return []byte{}, "", fmt.Errorf("X-GitHub-Event header missing")
	}

	signature := r.Header.Get("X-Hub-Signature")
	if signature == "" {
		return []byte{}, "", fmt.Errorf("X-Hub-Signature missing")
	}
	if !strings.HasPrefix(signature, "sha1=") {
		return []byte{}, "", fmt.Errorf("X-Hub-Signature does not start with sha1=")
	}
	want, err := hex.DecodeString(signature[len("sha1="):])
	if err != nil {
		return []byte{}, "", fmt.Errorf("Error decoding X-Hub-Signature: %v", err)
	}

	h := hmac.New(sha1.New, []byte(githubToken.Secret))
	// Intentionally check the HMAC first, only then attempt to decode JSON.
	body, err := ioutil.ReadAll(io.TeeReader(r.Body, h))
	if err != nil {
		return []byte{}, "", fmt.Errorf("Could not read body: %v", err)
	}
	got := h.Sum(nil)
	if !hmac.Equal(want, got) {
		log.Errorf(ctx, "X-Hub-Signature: want %x, got %x", want, got)
		return []byte{}, "", fmt.Errorf("X-Hub-Signature wrong")
	}

	return body, event, nil
}

func getRepoAndIssue(payload interface{}) (*github.Repository, *github.Issue) {
	switch v := payload.(type) {
	case github.IssueCommentEvent:
		return v.Repo, v.Issue
	case github.IssuesEvent:
		return v.Repo, v.Issue
	default:
		panic("Unknown type passed as payload")
	}
	return nil, nil
}

func addLabel(ctx context.Context, client *github.Client, payload interface{}, w http.ResponseWriter, newLabel string) bool {
	repo, issue := getRepoAndIssue(payload)

	// Avoid useless API requests.
	for _, label := range issue.Labels {
		if *label.Name == newLabel {
			return false
		}
	}

	_, resp, err := client.Issues.AddLabelsToIssue(
		ctx,
		*repo.Owner.Login,
		*repo.Name,
		*issue.Number,
		[]string{newLabel})
	if err != nil {
		http.Error(w, fmt.Sprintf("AddLabelsToIssue: %v", err), http.StatusInternalServerError)
		return false
	}
	discardResponse(resp)
	return true
}

func deleteLabel(ctx context.Context, client *github.Client, payload interface{}, w http.ResponseWriter, oldLabel string) bool {
	repo, issue := getRepoAndIssue(payload)

	// Avoid useless API requests.
	found := false
	for _, label := range issue.Labels {
		if *label.Name == oldLabel {
			found = true
			break
		}
	}
	if !found {
		return false
	}

	resp, err := client.Issues.RemoveLabelForIssue(
		ctx,
		*repo.Owner.Login,
		*repo.Name,
		*issue.Number,
		oldLabel)
	if err != nil {
		http.Error(w, fmt.Sprintf("RemoveLabelForIssue: %v", err), http.StatusInternalServerError)
		return false
	}
	discardResponse(resp)
	return true
}

func addComment(ctx context.Context, client *github.Client, payload interface{}, w http.ResponseWriter, comment string) bool {
	repo, issue := getRepoAndIssue(payload)
	_, resp, err := client.Issues.CreateComment(
		ctx,
		*repo.Owner.Login,
		*repo.Name,
		*issue.Number,
		&github.IssueComment{
			Body: github.String(comment),
		})
	if err != nil {
		http.Error(w, fmt.Sprintf("CreateComment: %v", err), http.StatusInternalServerError)
		return false
	}
	discardResponse(resp)
	return true
}

func getCompletedMilestones(ctx context.Context, client *github.Client, payload interface{}, w http.ResponseWriter) []*github.Milestone {
	repo, _ := getRepoAndIssue(payload)
	milestones, resp, err := client.Issues.ListMilestones(
		ctx,
		*repo.Owner.Login,
		*repo.Name,
		&github.MilestoneListOptions{
			State:     "closed",
			Sort:      "due_date",
			Direction: "desc",
		})
	if err != nil {
		http.Error(w, fmt.Sprintf("ListMilestones: %v", err), http.StatusInternalServerError)
		return nil
	}
	discardResponse(resp)
	return milestones
}

func closeIssue(ctx context.Context, client *github.Client, payload interface{}, w http.ResponseWriter) bool {
	repo, issue := getRepoAndIssue(payload)
	_, resp, err := client.Issues.Edit(
		ctx,
		*repo.Owner.Login,
		*repo.Name,
		*issue.Number,
		&github.IssueRequest{
			State:       github.String("closed"),
			StateReason: github.String("not_planned"),
		})
	if err != nil {
		http.Error(w, fmt.Sprintf("Edit: %v", err), http.StatusInternalServerError)
		return false
	}
	discardResponse(resp)
	return true
}

func issueCommentHandler(w http.ResponseWriter, r *http.Request) {
	ctx := appengine.NewContext(r)

	if err := getGitHubToken(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	body, event, err := readAndVerifyBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if event == "ping" {
		return
	}

	if event != "issue_comment" {
		http.Error(w, "Expected X-GitHub-Event: issue_comment", http.StatusBadRequest)
		return
	}

	var payload github.IssueCommentEvent
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, fmt.Sprintf("Cannot parse JSON: %v", err), http.StatusBadRequest)
		return
	}

	log.Infof(ctx, "request: %+v", r)
	log.Infof(ctx, "payload: %+v", payload)

	// Wrap the urlfetch.Transport with our User-Agent and authentication.
	transport := githubTransport(urlfetch.Transport{Context: ctx})
	githubclient := github.NewClient(&http.Client{Transport: &transport})

	// We only act in case the comment is by the issue creator.
	if *payload.Issue.User.Login != *payload.Comment.User.Login {
		return
	}

	// See if any labels need to be removed.
	currentLabels := make(map[string]bool)
	for _, label := range payload.Issue.Labels {
		currentLabels[*label.Name] = true
	}
	if !currentLabels["missing-version"] &&
		!currentLabels["unsupported-version"] &&
		!currentLabels["missing-log"] {
		return
	}

	if currentLabels["missing-log"] {
		if strings.Contains(*payload.Comment.Body, "://logs.i3wm.org") {
			deleteLabel(ctx, githubclient, payload, w, "missing-log")
		}
	}

	if currentLabels["missing-version"] || currentLabels["unsupported-version"] {
		matches := extractVersion(*payload.Comment.Body)
		if len(matches) == 0 {
			return
		}
		// TODO: point to the other repositories if payload.Repo.Name != matches[1]

		log.Infof(ctx, "matches: %v", matches)

		deleteLabel(ctx, githubclient, payload, w, "missing-version")

		// We only verify the major version for i3 itself, not for i3status or
		// i3lock (those bugs are not filed in the right repository anyway, but
		// people still do that…).
		if matches[1] != "i3" {
			return
		}

		// Verify the major version is recent enough to be supported.
		milestones := getCompletedMilestones(ctx, githubclient, payload, w)
		if len(milestones) == 0 {
			return
		}

		majorVersion := matches[2]
		for strings.HasSuffix(majorVersion, ".") {
			majorVersion = majorVersion[:len(majorVersion)-1]
		}

		if *milestones[0].Title != majorVersion {
			if addLabel(ctx, githubclient, payload, w, "unsupported-version") {
				addComment(ctx, githubclient, payload, w, fmt.Sprintf(
					"Sorry, we can only support the latest major version. "+
						"Please upgrade from %s to %s, verify the bug still exists, "+
						"and re-open this issue.", majorVersion, *milestones[0].Title))
				closeIssue(ctx, githubclient, payload, w)
			}
			return
		}

		addLabel(ctx, githubclient, payload, w, *milestones[0].Title)
		deleteLabel(ctx, githubclient, payload, w, "unsupported-version")
	}
}

func issuesHandler(w http.ResponseWriter, r *http.Request) {
	ctx := appengine.NewContext(r)

	if err := getGitHubToken(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	body, event, err := readAndVerifyBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if event == "ping" {
		return
	}

	if event != "issues" {
		http.Error(w, "Expected X-GitHub-Event: issues", http.StatusBadRequest)
		return
	}

	var payload github.IssuesEvent
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, fmt.Sprintf("Cannot parse JSON: %v", err), http.StatusBadRequest)
		return
	}

	if *payload.Action != "opened" {
		return
	}

	log.Infof(ctx, "request: %+v", r)
	log.Infof(ctx, "payload: %+v", payload)

	// Wrap the urlfetch.Transport with our User-Agent and authentication.
	transport := githubTransport(urlfetch.Transport{Context: ctx})
	githubclient := github.NewClient(&http.Client{Transport: &transport})

	lcBody := strings.ToLower(*payload.Issue.Body)
	lcTitle := strings.ToLower(*payload.Issue.Title)

	if *payload.Action == "opened" {
		if enhancementRegexp.MatchString(lcBody) || enhancementRegexpTitle.MatchString(lcTitle) {
			// For feature requests, add the enhancement label, but only on creation.
			// Skip all the other checks.
			addLabel(ctx, githubclient, payload, w, "enhancement")

			if newConfigurationRegexp.MatchString(lcBody) {
				addLabel(ctx, githubclient, payload, w, "requires-configuration")
			}

			addComment(ctx, githubclient, payload, w, "Please note that new features which require additional configuration will usually not be considered. We are happy with the feature set of i3 and want to focus in fixing bugs instead. We do accept feature requests, however, and will evaluate whether the added benefit (clearly) outweighs the complexity it adds to i3.\n\nKeep in mind that i3 provides a powerful way to interact with it through its IPC interface: https://i3wm.org/docs/ipc.html.")

			return
		}

		if documentationRegexp.MatchString(lcBody) {
			// Same for documentation requests.
			addLabel(ctx, githubclient, payload, w, "documentation")
			return
		}

		if bugRegexp.MatchString(lcBody) {
			addLabel(ctx, githubclient, payload, w, "bug")
		}
	}

	// TODO: be a bit smarter about this if it turns out that people use
	// something else than logs.i3wm.org a lot. we could HEAD all URLs, then
	// request just enough bytes to see if the file is a bzip2 file (and
	// reasonably small), then download the rest, uncompress, and see whether
	// it’s an i3 log
	if !strings.Contains(lcBody, "://logs.i3wm.org") {
		if addLabel(ctx, githubclient, payload, w, "missing-log") {
			addComment(ctx, githubclient, payload, w, "I don’t see a link to logs.i3wm.org. "+
				"Did you follow https://i3wm.org/docs/debugging.html? "+
				"(In case you actually provided a link to a logfile, please ignore me.)")
		}
	}

	matches := extractVersion(*payload.Issue.Body)
	if len(matches) == 0 {
		if addLabel(ctx, githubclient, payload, w, "missing-version") {
			addComment(ctx, githubclient, payload, w, "I don’t see a version number. "+
				"Could you please copy & paste the output of `i3 --version` into this issue?")
		}
		return
	}
	// TODO: point to the other repositories if payload.Repo.Name != matches[1]

	// We only verify the major version for i3 itself, not for i3status or
	// i3lock (those bugs are not filed in the right repository anyway, but
	// people still do that…).
	if matches[1] != "i3" {
		return
	}

	// Verify the major version is recent enough to be supported.
	milestones := getCompletedMilestones(ctx, githubclient, payload, w)
	if len(milestones) == 0 {
		log.Errorf(ctx, "No milestones found")
		return
	}

	majorVersion := matches[2]
	for strings.HasSuffix(majorVersion, ".") {
		majorVersion = majorVersion[:len(majorVersion)-1]
	}

	if *milestones[0].Title != majorVersion {
		if addLabel(ctx, githubclient, payload, w, "unsupported-version") {
			addComment(ctx, githubclient, payload, w, fmt.Sprintf(
				"Sorry, we can only support the latest major version. "+
					"Please upgrade from %s to %s, verify the bug still exists, "+
					"and re-open this issue.", majorVersion, *milestones[0].Title))
			closeIssue(ctx, githubclient, payload, w)
		}
		return
	}
	addLabel(ctx, githubclient, payload, w, *milestones[0].Title)
}
