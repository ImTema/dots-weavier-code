//go:build !unix

package main

// maxRSS is empty where getrusage does not exist: the debug line shows wall time only.
func maxRSS() string { return "" }
