package app

// Host is the ssh host whose daemon the window shows, set by main for
// pitwall --host; "" is this machine. Paths in the state are the host's
// then, so the window reads no files, runs no git and opens no file links
// for them here.
var Host string
