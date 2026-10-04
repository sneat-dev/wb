package main

import "github.com/sneat-dev/wb/internal/browser"

func openBrowser(target string) error { return browser.Open(target) }
