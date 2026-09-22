package player

import "math/rand/v2"

// ChooseStart picks the first track. An explicit prefer always wins when it
// is in the queue, including tracks already in history. Automatic choice
// (empty prefer) skips history until it is cleared.
func ChooseStart(queue []string, prefer string, skip map[string]struct{}, random bool) (index int, ok bool) {
	if prefer != "" {
		for i, p := range queue {
			if p == prefer {
				return i, true
			}
		}
	}
	_, index, ok = NextUnplayed(queue, -1, skip, random, 1)
	return index, ok
}

// HasUnplayed reports whether any queue path is absent from skip.
func HasUnplayed(queue []string, skip map[string]struct{}) bool {
	for _, p := range queue {
		if _, played := skip[p]; !played {
			return true
		}
	}
	return false
}

// NextUnplayed walks the queue skipping paths in skip. dir > 0 goes forward,
// dir < 0 backward. random ignores direction and picks any remaining track.
func NextUnplayed(queue []string, from int, skip map[string]struct{}, random bool, dir int) (path string, index int, ok bool) {
	candidates := make([]int, 0, len(queue))
	for i, p := range queue {
		if _, played := skip[p]; played {
			continue
		}
		candidates = append(candidates, i)
	}
	if len(candidates) == 0 {
		return "", 0, false
	}
	if random {
		i := candidates[rand.IntN(len(candidates))]
		return queue[i], i, true
	}
	if dir >= 0 {
		for _, i := range candidates {
			if i > from {
				return queue[i], i, true
			}
		}
		return "", 0, false
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		idx := candidates[i]
		if idx < from {
			return queue[idx], idx, true
		}
	}
	return "", 0, false
}
