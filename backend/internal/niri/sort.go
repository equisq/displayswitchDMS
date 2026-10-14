package niri

import "sort"

func sortOutputs(o []Output) {
	sort.Slice(o, func(i, j int) bool { return o[i].Name < o[j].Name })
}
