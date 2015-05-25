i3-github-bot is a [GitHub web hook](https://developer.github.com/webhooks/)
running on [Google AppEngine](https://cloud.google.com/appengine/). It replaces
[i3-new-ticket](http://code.stapelberg.de/git/i3-new-ticket/) which we used
before migrating to GitHub.

The purpose of i3-github-bot is to ensure that we get high-quality bug reports
and to automate as much of the standard communication about bugs as possible,
i.e. we want to automatically make people aware that they need to tell us the
i3 version number they’re using and supply us a debug log file so that we can
see what’s going on. Before writing i3-new-ticket, these were by far the most
common interactions on newly reported bugs, and automating that part of the
reporting process is valuable for the user and for our developers.

In addition to automated checks of the version number, we also provide a tiny
service to host i3 debug log files, since GitHub does not allow attachments at
the time of writing. See
[i3/docs/debugging](http://i3wm.org/docs/debugging.html) for usage instructions.

To deploy a new version, use `goapp deploy app.yaml` from the [Google App
Engine SDK for Go](https://cloud.google.com/appengine/downloads)
