package githubbot

import "testing"

func TestVersion1640(t *testing.T) {
	body := `
**TL;DR:** Just running ` + "`make`" + ` and omitting ` + "`make clean`" + ` aparently may result in mix-match of binaries that (apart from other potential problems) may report the older version.

Happened after checking out commit eb04a64 and re-building, with left-over binaries from tag 4.10.1.  Tree clean in both cases; Fedora 21 w/ git-2.1.0-4.fc21.x86_64 and gcc-4.9.2-6.fc21.x86_64.

---

I came to my machine (with i3 built from 4.10.1 tag running) with intent to quickly verify a bug fixed few hours ago.

    # cd'd to the i3 git repo
    git checkout master
    make
    sudo make install

Then restarted w/ $mod+R (actually twice, as the verification required).  I could immediately see the effect of the changed behavior so that i3 was definitely reloaded.

But, out of curiosity I ran ` + "`i3 --moreversion`" + ` and to my surprise, both reported versions were 4.10.1, just as before restarting!

    Binary i3 version:  4.10.1 (2015-03-29, branch "4.10.1") © 2009-2014 Michael Stapelberg and contributors
    Running i3 version: 4.10.1 (2015-03-29, branch "4.10.1") (pid 1552)

So I got back to the git repo, and this time ran ` + "`make clean`" + ` before ` + "`make`" + `, and I could immediately see that many more binaries were getting built. After installing and reloading, versions were right:

    Binary i3 version:  4.10.1-6-geb04a64 (2015-04-06, branch "master") © 2009-2014 Michael Stapelberg and contributors
    Running i3 version: 4.10.1-6-geb04a64 (2015-04-06, branch "master") (pid 1552)

I guess this could lead to pretty strange situations with misleading data, if anybody uses the output for bug reporting.
`
	matches := extractVersion(body)
	if len(matches) < 3 || matches[1] != "i3" || matches[2] != "4.10" {
		t.Fatalf("Issue #1640 not recognized properly, matches = %+v", matches)
	}
}

func TestVersion1694(t *testing.T) {
	body := `
i3 >= 4.8 doesn't play nice with xfce4-panel (=4.10.1) anymore.

I normally start i3 under xfce4-session (=4.12.1) with

` + "```bash" + `
pkill xfwm4
nohup i3 > /dev/null &
` + "```" + `

and this worked great with i3 4.7.* I was running until recently, i3bar covering the lower xfce4-panel whenever ` + "Mod" + ` key was held.

Now with 4.8, or even

` + "```" + `
Binary i3 version:  4.10.2 (2015-04-16, branch "4.10.2")
Running i3 version: 4.10.2 (2015-04-16, branch "4.10.2")
` + "```" + `

the xfce4-panel disappears after a few workspace switches. It is just not there, invisible. The panel process is still running and responding the xfce4-whiskermenu-plugin still pops up on its binding, but the window is positioned in +0+0 corner.

This happens with a default generated i3 config with only one line added:

` + "```" + `
exec --no-startup-id xfce4-panel --disable-wm-check
` + "```" + `

The log file that records the disappearance of the panel: https://logs.i3wm.org/logs/5745865499082752.bz2

Behavior is the same under xfce4-session as well as i3-with-shmlog xsession.

How do I go further with debugging this? Can you confirm the bug?
`
	matches := extractVersion(body)
	if len(matches) < 3 || matches[1] != "i3" || matches[2] != "4.10" {
		t.Fatalf("Issue #1694 not recognized properly, matches = %+v", matches)
	}
}

func TestLogFalsePositive(t *testing.T) {
	body := `
Here is an extract from my log:

` + "```" + `
03/28/2015 10:21:22 PM - config_parser.c:parse_config:313 - CONFIG(line 22): # Before i3 v4.8, we used to recommend this one as the default:
` + "```" + `

Not sure which version it is, though.
`
	matches := extractVersion(body)
	if len(matches) > 0 {
		t.Fatalf("logfile matched (false positive)")
	}
}
