package githubbot

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/go-github/github"

	"appengine"
	"appengine/datastore"
	"appengine/urlfetch"
	"appengine/user"
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
	reMajorVersion = regexp.MustCompile(`(i3|i3status|i3lock):?\s*(?:version|v|vers|ver)?:?\s*(3\.[a-e]|3\.\p{Greek}|[0-9]\.[0-9]+)`)
)

func init() {
	http.HandleFunc("/issues", issuesHandler)
	http.HandleFunc("/issue_comment", issueCommentHandler)
	http.HandleFunc("/update_github_token", updateTokenHandler)
}

func updateTokenHandler(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	u := user.Current(c)
	if u == nil {
		url, err := user.LoginURL(c, "/update_github_token")
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

	if err := getGitHubToken(c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Method == "POST" {
		k := datastore.NewKey(c, "GitHubToken", "githubtoken", 0, nil)
		t := GitHubToken{
			Token:  r.FormValue("token"),
			Secret: r.FormValue("secret"),
		}
		if _, err := datastore.Put(c, k, &t); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		githubToken = t
	}
	fmt.Fprintf(w, updateTokenForm, githubToken.Token, githubToken.Secret)
}

func getGitHubToken(c appengine.Context) error {
	if githubToken.Secret != "" && githubToken.Token != "" {
		return nil
	}
	k := datastore.NewKey(c, "GitHubToken", "githubtoken", 0, nil)
	return datastore.Get(c, k, &githubToken)
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
	c := appengine.NewContext(r)

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
		c.Errorf("X-Hub-Signature: want %x, got %x", want, got)
		return []byte{}, "", fmt.Errorf("X-Hub-Signature wrong")
	}

	return body, event, nil
}

func getRepoAndIssue(payload interface{}) (*github.Repository, *github.Issue) {
	switch v := payload.(type) {
	case github.IssueCommentEvent:
		return v.Repo, v.Issue
	case github.IssueActivityEvent:
		return v.Repo, v.Issue
	default:
		log.Panicf("Unknown type passed as payload")
	}
	return nil, nil
}

func addLabel(client *github.Client, payload interface{}, w http.ResponseWriter, newLabel string) bool {
	repo, issue := getRepoAndIssue(payload)

	// Avoid useless API requests.
	for _, label := range issue.Labels {
		if *label.Name == newLabel {
			return false
		}
	}

	_, resp, err := client.Issues.AddLabelsToIssue(
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

func deleteLabel(client *github.Client, payload interface{}, w http.ResponseWriter, oldLabel string) bool {
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

func addComment(client *github.Client, payload interface{}, w http.ResponseWriter, comment string) bool {
	repo, issue := getRepoAndIssue(payload)
	_, resp, err := client.Issues.CreateComment(
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

func getMilestones(client *github.Client, payload interface{}, w http.ResponseWriter) []github.Milestone {
	repo, _ := getRepoAndIssue(payload)
	milestones, resp, err := client.Issues.ListMilestones(
		*repo.Owner.Login,
		*repo.Name,
		&github.MilestoneListOptions{
			Sort:      "due_date",
			Direction: "desc",
		})
	if err != nil {
		http.Error(w, fmt.Sprintf("ListMilestones: %v", err), http.StatusInternalServerError)
		return []github.Milestone{}
	}
	discardResponse(resp)
	return milestones
}

func closeIssue(client *github.Client, payload interface{}, w http.ResponseWriter) bool {
	repo, issue := getRepoAndIssue(payload)
	_, resp, err := client.Issues.Edit(
		*repo.Owner.Login,
		*repo.Name,
		*issue.Number,
		&github.IssueRequest{
			State: github.String("closed"),
		})
	if err != nil {
		http.Error(w, fmt.Sprintf("Edit: %v", err), http.StatusInternalServerError)
		return false
	}
	discardResponse(resp)
	return true
}

func issueCommentHandler(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)

	if err := getGitHubToken(c); err != nil {
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

	c.Infof("request: %+v", r)
	c.Infof("payload: %+v", payload)

	// Wrap the urlfetch.Transport with our User-Agent and authentication.
	transport := githubTransport(urlfetch.Transport{Context: c})
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
		if strings.Contains(*payload.Comment.Body, "http://logs.i3wm.org") {
			deleteLabel(githubclient, payload, w, "missing-log")
		}
	}

	if currentLabels["missing-version"] || currentLabels["unsupported-version"] {
		matches := reMajorVersion.FindStringSubmatch(*payload.Comment.Body)
		if len(matches) == 0 {
			return
		}
		// TODO: point to the other repositories if payload.Repo.Name != matches[1]

		c.Infof("matches: %v", matches)

		deleteLabel(githubclient, payload, w, "missing-version")

		// We only verify the major version for i3 itself, not for i3status or
		// i3lock (those bugs are not filed in the right repository anyway, but
		// people still do that…).
		if matches[1] != "i3" {
			return
		}

		// Verify the major version is recent enough to be supported.
		milestones := getMilestones(githubclient, payload, w)
		if len(milestones) == 0 {
			return
		}

		majorVersion := matches[2]
		for strings.HasSuffix(majorVersion, ".") {
			majorVersion = majorVersion[:len(majorVersion)-1]
		}

		if *milestones[0].Title != majorVersion {
			if addLabel(githubclient, payload, w, "unsupported-version") {
				addComment(githubclient, payload, w, fmt.Sprintf(
					"Sorry, we can only support the latest major version. "+
						"Please upgrade from %s to %s, verify the bug still exists, "+
						"and re-open this issue.", majorVersion, *milestones[0].Title))
				closeIssue(githubclient, payload, w)
			}
			return
		}

		addLabel(githubclient, payload, w, *milestones[0].Title)
		deleteLabel(githubclient, payload, w, "unsupported-version")
	}
}

func issuesHandler(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)

	if err := getGitHubToken(c); err != nil {
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

	var payload github.IssueActivityEvent
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, fmt.Sprintf("Cannot parse JSON: %v", err), http.StatusBadRequest)
		return
	}

	if *payload.Action != "opened" {
		return
	}

	c.Infof("request: %+v", r)
	c.Infof("payload: %+v", payload)

	// Wrap the urlfetch.Transport with our User-Agent and authentication.
	transport := githubTransport(urlfetch.Transport{Context: c})
	githubclient := github.NewClient(&http.Client{Transport: &transport})

	if *payload.Action == "opened" &&
		(strings.Contains(*payload.Issue.Body, "enhancement") ||
			strings.Contains(*payload.Issue.Body, "feature request") ||
			strings.Contains(*payload.Issue.Title, "enhancement") ||
			strings.Contains(*payload.Issue.Title, "feature request")) {
		// For feature requests, add the enhancement label, but only on creation.
		// Skip all the other checks.
		addLabel(githubclient, payload, w, "enhancement")
		return
	}

	// TODO: be a bit smarter about this if it turns out that people use
	// something else than logs.i3wm.org a lot. we could HEAD all URLs, then
	// request just enough bytes to see if the file is a bzip2 file (and
	// reasonably small), then download the rest, uncompress, and see whether
	// it’s an i3 log
	if !strings.Contains(*payload.Issue.Body, "http://logs.i3wm.org") {
		if addLabel(githubclient, payload, w, "missing-log") {
			addComment(githubclient, payload, w, "I don’t see a link to logs.i3wm.org. "+
				"Did you follow http://i3wm.org/docs/debugging.html? "+
				"(In case you actually provided a link to a logfile, please ignore me.)")
		}
	}

	matches := reMajorVersion.FindStringSubmatch(*payload.Issue.Body)
	if len(matches) == 0 {
		if addLabel(githubclient, payload, w, "missing-version") {
			addComment(githubclient, payload, w, "I don’t see a version number. "+
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
	milestones := getMilestones(githubclient, payload, w)
	if len(milestones) == 0 {
		c.Errorf("No milestones found")
		return
	}

	majorVersion := matches[2]
	for strings.HasSuffix(majorVersion, ".") {
		majorVersion = majorVersion[:len(majorVersion)-1]
	}

	if *milestones[0].Title != majorVersion {
		if addLabel(githubclient, payload, w, "unsupported-version") {
			addComment(githubclient, payload, w, fmt.Sprintf(
				"Sorry, we can only support the latest major version. "+
					"Please upgrade from %s to %s, verify the bug still exists, "+
					"and re-open this issue.", majorVersion, *milestones[0].Title))
			closeIssue(githubclient, payload, w)
		}
		return
	}
	addLabel(githubclient, payload, w, *milestones[0].Title)
}
