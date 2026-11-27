package tunables

import "reflect"

// groupKind is what a struct type that the page draws in a way of its own is: an ordinary struct is a group with its
// fields in a card.
type groupKind string

const (
	// kindRule is a struct of two sets that go side by side in one card (Only and Except). The options of its tag that
	// describe the values (enum, min, max, precision, minlen, maxlen, source, custom) are the options of both sets.
	kindRule groupKind = "rule"
	// kindFeature is a struct with a switch ("enabled") and the rules of who it is on for, which the page adds from a list
	// and folds away while the switch is off. The rules are in a field tagged `tune:"flatten:true"`, so they are right in
	// the feature, beside the switch.
	kindFeature groupKind = "feature"
)

type kinded interface{ groupKind() groupKind }

func kindOf(t reflect.Type) groupKind {
	if k, ok := reflect.Zero(t).Interface().(kinded); ok {
		return k.groupKind()
	}
	return ""
}
