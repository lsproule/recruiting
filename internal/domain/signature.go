package domain

import (
	"fmt"
	"regexp"
	"strings"

	"recruiting/runner/wire"
)

var identifier = regexp.MustCompile(`^[a-z][a-zA-Z0-9_]*$`)

// ValidateSignature is every reason a signature cannot be used. A name has
// to be an identifier every language accepts and no language's keyword, so
// the generated driver and stub compile everywhere; parameter names must
// be distinct and not shadow the function.
func ValidateSignature(s Signature) []string {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	switch {
	case s.Name == "":
		add("the function needs a name")
	case !identifier.MatchString(s.Name):
		add("function name %q must start with a lower-case letter and use only letters, digits, and underscores", s.Name)
	case reservedWord[s.Name]:
		add("function name %q is a keyword in one of the languages", s.Name)
	}
	if len(s.Params) > wire.MaxSignatureParams {
		add("a function takes at most %d parameters, this one declares %d", wire.MaxSignatureParams, len(s.Params))
	}
	seen := map[string]bool{}
	for i, p := range s.Params {
		switch {
		case p.Name == "":
			add("parameter %d needs a name", i+1)
		case !identifier.MatchString(p.Name):
			add("parameter name %q must start with a lower-case letter and use only letters, digits, and underscores", p.Name)
		case p.Name == s.Name:
			add("parameter %q cannot share the function's name", p.Name)
		case seen[p.Name]:
			add("parameter %q is declared twice", p.Name)
		case reservedWord[p.Name]:
			add("parameter %q is a keyword in one of the languages", p.Name)
		}
		seen[p.Name] = true
		if !contains(SignatureTypes, p.Type) {
			add("parameter %q has unknown type %q (want one of %s)", p.Name, p.Type, strings.Join(SignatureTypes, ", "))
		}
	}
	if !contains(SignatureTypes, s.Returns) {
		add("return type %q is unknown (want one of %s)", s.Returns, strings.Join(SignatureTypes, ", "))
	}
	return errs
}

// reservedWord is the union of keywords and common built-ins across the
// ten languages that would break a generated driver or stub as a name.
var reservedWord = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
		abstract alias and any array as assert async auto await base begin bool boolean break byte case catch chan char
		checked class clone const constexpr continue crate decimal declare def default defer del delete dict do double
		dyn echo elif else elsif end endif enum event except exit explicit export extends extern fallthrough false final
		finally fixed float fn for foreach friend func function global go goto hash if impl implements import in
		inline input instanceof int interface internal is lambda len let list lock long loop main map match max min
		mod module move mut namespace native new next nil none noexcept not null nullptr object of operator or out
		override package params pass print private protected pub public raise range readonly redo ref register
		restrict result retry return sbyte sealed select self set short signed sizeof solution stackalloc static std
		str string struct sum super switch synchronized template then this throw throws trait transient true try type
		typedef typename typeof uint ulong unchecked undef union unless unsafe unsigned until use ushort using var
		vector virtual void volatile when where while with yield`) {
		reservedWord[w] = true
	}
}
