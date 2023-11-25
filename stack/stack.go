package stack

// Credit: Michael Green
// https://stackoverflow.com/questions/28541609/looking-for-reasonable-Stack-implementation-in-golang
type Stack[T any] struct {
	Push   func(T)
	Pop    func() T
	Peek   func() T
	Length func() int
}

func NewStack[T any]() Stack[T] {
	slice := make([]T, 0)
	return Stack[T]{
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
