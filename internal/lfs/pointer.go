package lfs

import "github.com/define42/GitOne/internal/lfspointer"

const MaxPointerSize = lfspointer.MaxPointerSize

type Pointer = lfspointer.Pointer

func ParsePointer(content []byte) (Pointer, bool) {
	return lfspointer.Parse(content)
}
