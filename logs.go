package githubbot

import (
	"bytes"
	"compress/bzip2"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/context"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
	"google.golang.org/appengine/file"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/urlfetch"
	"google.golang.org/cloud"
	"google.golang.org/cloud/storage"
)

const (
	fileName   = `[a-zA-Z0-9-_/.]+\.[ch]`
	identifier = `[_a-zA-Z][_a-zA-Z0-9]{0,30}`
	lineNumber = `[0-9]+`
)

// Matches an i3 log line, such as:
// 2015-02-01 17:21:48 - ../i3-4.8/src/handlers.c:handle_event:1231 - blah
// (cannot match the date/time since that is locale-specific)
var i3LogLine = regexp.MustCompile(` - ` + fileName + `:` + identifier + `:` + lineNumber + ` - `)

type Blobref struct {
	Blobkey  appengine.BlobKey
	Filename string
}

func init() {
	http.HandleFunc("/", logHandler)
	http.HandleFunc("/logs/", logsHandler)
}

func logsHandler(w http.ResponseWriter, r *http.Request) {
	var blobref Blobref

	c := appengine.NewContext(r)

	strid := path.Base(r.URL.Path)
	if strings.HasSuffix(strid, ".bz2") {
		strid = strid[:len(strid)-len(".bz2")]
	}

	intid, err := strconv.ParseInt(strid, 0, 64)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := datastore.Get(c, datastore.NewKey(c, "blobref", "", intid, nil), &blobref); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if blobref.Blobkey != "" {
		// TODO: remove this code path once we migrated all objects to GCS
		hdr := w.Header()
		hdr.Set("X-AppEngine-BlobKey", string(blobref.Blobkey))

		if hdr.Get("Content-Type") == "" {
			// This value is known to dev_appserver to mean automatic.
			// In production this is remapped to the empty value which
			// means automatic.
			hdr.Set("Content-Type", "application/vnd.google.appengine.auto")
		}
	} else {
		hc := &http.Client{
			Transport: &oauth2.Transport{
				Source: google.AppEngineTokenSource(c, storage.ScopeFullControl),
				Base:   &urlfetch.Transport{Context: c},
			},
		}
		ctx := cloud.NewContext(appengine.AppID(c), hc)
		bucket, err := file.DefaultBucketName(c)
		if err != nil {
			log.Errorf(ctx, "default bucket: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		rc, err := storage.NewReader(ctx, bucket, blobref.Filename)
		if err != nil {
			log.Errorf(ctx, "NewReader: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		if _, err := io.Copy(w, rc); err != nil {
			log.Errorf(ctx, "Copy: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

}

func writeBlob(c context.Context, r io.Reader) (string, error) {
	filename := strconv.FormatInt(time.Now().UnixNano(), 10)
	hc := &http.Client{
		Transport: &oauth2.Transport{
			Source: google.AppEngineTokenSource(c, storage.ScopeFullControl),
			Base:   &urlfetch.Transport{Context: c},
		},
	}
	ctx := cloud.NewContext(appengine.AppID(c), hc)
	bucket, err := file.DefaultBucketName(c)
	if err != nil {
		return "", err
	}
	bw := storage.NewWriter(ctx, bucket, filename)
	bw.ContentType = "application/octet-stream"
	bw.ACL = []storage.ACLRule{{storage.AllUsers, storage.RoleReader}}
	if _, err := io.Copy(bw, r); err != nil {
		return "", err
	}
	if err := bw.Close(); err != nil {
		return "", err
	}

	return filename, nil
}

// TODO: wrap this so that errors contain an instruction on how to use the service.
// logHandler takes a compressed i3 debug log and stores it on
// Google Cloud Storage.
func logHandler(w http.ResponseWriter, r *http.Request) {
	var body bytes.Buffer
	rd := bzip2.NewReader(io.TeeReader(r.Body, &body))
	uncompressed, err := ioutil.ReadAll(rd)
	if err != nil {
		http.Error(w, "Data not bzip2-compressed.", http.StatusBadRequest)
		return
	}

	// TODO: match line by line, and have a certain percentage that needs to be an i3 log
	// TODO: also allow strace log files
	if !i3LogLine.Match(uncompressed) {
		http.Error(w, "Data is not an i3 log file.", http.StatusBadRequest)
		return
	}

	c := appengine.NewContext(r)

	filename, err := writeBlob(c, &body)
	if err != nil {
		http.Error(w, fmt.Sprintf("cloud storage: %v", err), http.StatusInternalServerError)
		return
	}

	key, err := datastore.Put(c, datastore.NewIncompleteKey(c, "blobref", nil), &Blobref{Filename: filename})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprintf(w, "http://logs.i3wm.org/logs/%d.bz2\n", key.IntID())
}
