package output

type StructuredStreamer interface {
	StructuredStream(v any, err error)
}

type streamerWriter struct {
	*output
	isStderr bool
}

func (s *streamerWriter) StructuredStream(v any, err error) {
	obj := object{
		Cmd: s.cmd,
	}

	if s.isStderr {
		obj.Stderr = v
	} else {
		obj.Stdout = v
	}

	if err != nil {
		obj.Err = err.Error()
	}

	_ = s.jsonEncoder.Encode(obj)
}

func (s *streamerWriter) Write(p []byte) (int, error) {
	if s.isStderr {
		return s.stderr.Write(p)
	}

	return s.stdout.Write(p)
}
