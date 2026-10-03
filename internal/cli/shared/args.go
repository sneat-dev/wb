package shared

func ArgumentOrCurrent(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return "."
}
