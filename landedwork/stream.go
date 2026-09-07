package landedwork

import (
	"bytes"
	"errors"
)

// Git writes arbitrary chunks. Retain an ID only after charging its record and
// node, and retain at most one unfinished ID between writes.
type commitStream struct {
	meter   *meter
	ids     []string
	pending string
}

func (s *commitStream) Write(p []byte) (int, error) {
	if err := s.meter.input(int64(len(p))); err != nil {
		return 0, err
	}
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			if len(s.pending)+len(p) > 64 {
				return 0, errors.New("invalid revision object ID")
			}
			s.pending += string(p)
			break
		}
		if len(s.pending)+end > 64 {
			return 0, errors.New("invalid revision object ID")
		}
		if err := s.meter.node(); err != nil {
			return 0, err
		}
		if err := s.meter.records(1); err != nil {
			return 0, err
		}
		id := s.pending + string(p[:end])
		if !objectID(id) {
			return 0, errors.New("invalid revision object ID")
		}
		s.ids = append(s.ids, id)
		s.pending = ""
		p = p[end+1:]
	}
	return n, nil
}
