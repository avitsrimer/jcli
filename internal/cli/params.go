package cli

import "strings"

// paramPrefix is the argv prefix that marks a dynamic build parameter, e.g. --param-branch=main.
const paramPrefix = "--param-"

// extractParams scans argv for --param-<name>=<value> arguments, lifting each into a map keyed by
// <name> and returning the remaining argv (in original order) for go-flags to parse. The value is
// split on the FIRST '=', so '=' inside the value (e.g. --param-q=a=b) is preserved. An argument
// of the form --param-<name> with no '=' is left in the remaining argv untouched, as are all other
// non-matching arguments. A bare "--" terminator and everything after it is passed through verbatim
// so a literal --param-x=y intended as a positional after "--" is not consumed. The returned map is
// non-nil only when at least one param was found.
func extractParams(argv []string) (params map[string]string, rest []string) {
	rest = make([]string, 0, len(argv))
	for i, arg := range argv {
		if arg == "--" {
			// double-dash terminator: stop lifting params, pass it and the remainder through.
			rest = append(rest, argv[i:]...)
			break
		}
		name, value, ok := splitParam(arg)
		if !ok {
			rest = append(rest, arg)
			continue
		}
		if params == nil {
			params = make(map[string]string)
		}
		params[name] = value
	}
	return params, rest
}

// splitParam reports whether arg is a --param-<name>=<value> argument and, if so, returns the name
// and value split on the first '='. A bare --param- prefix without a name, or a prefix with no '=',
// is not a match.
func splitParam(arg string) (name, value string, ok bool) {
	if !strings.HasPrefix(arg, paramPrefix) {
		return "", "", false
	}
	body := arg[len(paramPrefix):]
	eq := strings.IndexByte(body, '=')
	if eq <= 0 {
		// no '=' (bare flag) or empty name (--param-=v): not a usable param assignment.
		return "", "", false
	}
	return body[:eq], body[eq+1:], true
}
