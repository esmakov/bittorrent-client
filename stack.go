package main
// Credit: Michael Green
// https://stackoverflow.com/questions/28541609/looking-for-reasonable-stack-implementation-in-golang
type stack[T any] struct {
	Push   func(T)
	Pop    func() T
	Peek   func() T
	Length func() int
}

func newStack[T any]() stack[T] {
	slice := make([]T, 0)
	return stack[T]{
		Push: func(i T) {
			slice = append(slice, i)
		},
		Pop: func() T {
			if len(slice) == 0 {
				return *new(T)
			}
			res := slice[len(slice)-1]
			slice = slice[:len(slice)-1]
			return res
		},
		Peek: func() T {
			if len(slice) == 0 {
				return *new(T)
			}

			res := slice[len(slice)-1]
			return res

		},
		Length: func() int {
			return len(slice)
		},
	}
}
