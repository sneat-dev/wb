package main

func usageError(message string) error {
	return &exitError{code: exitUsage, message: message}
}
