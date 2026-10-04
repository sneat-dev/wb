package main

import "errors"

// errBoomForCmdWB is shared by root filesystem transaction failure assertions.
var errBoomForCmdWB = errors.New("boom")
