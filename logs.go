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

	"appengine"
	"appengine/blobstore"
	"appengine/datastore"
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
	Blobkey appengine.BlobKey
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

	blobstore.Send(w, blobref.Blobkey)
}

func writeBlob(c appengine.Context, r io.Reader) (appengine.BlobKey, error) {
	bw, err := blobstore.Create(c, "application/octet-stream")
	if err != nil {
		return appengine.BlobKey(""), err
	}
	if _, err := io.Copy(bw, r); err != nil {
		return appengine.BlobKey(""), err
	}
	if err := bw.Close(); err != nil {
		return appengine.BlobKey(""), err
	}

	key, err := bw.Key()
	return key, err
}

// TODO: wrap this so that errors contain an instruction on how to use the service.
// logHandler takes a compressed i3 debug log and stores it as a Gist on
// GitHub.
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

	blobkey, err := writeBlob(c, &body)
	if err != nil {
		http.Error(w, fmt.Sprintf("blobstore: %v", err), http.StatusInternalServerError)
		return
	}

	key, err := datastore.Put(c, datastore.NewIncompleteKey(c, "blobref", nil), &Blobref{blobkey})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprintf(w, "http://logs.i3wm.org/logs/%d.bz2\n", key.IntID())
}
