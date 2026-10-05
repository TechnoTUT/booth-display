package pipeline

// maxAccessUnitSize is a safety cap used when the encoder does not emit AUDs.
const maxAccessUnitSize = 2 * 1024 * 1024

// AccessUnit is one complete coded picture: all NAL units (optional SPS/PPS/SEI
// plus every slice) that belong to a single frame, in Annex-B form.
type AccessUnit struct {
	Data       []byte
	IsKeyframe bool
}

// AccessUnitAssembler groups a NAL unit stream into Access Units.
//
// Boundaries are detected with Access Unit Delimiters (NAL type 9), which the
// encoder must be configured to emit (x264: aud=1). Receivers such as Android
// MediaCodec require a whole picture per input buffer, so splitting a frame's
// slices into separate packets-of-frames prevents decoding.
type AccessUnitAssembler struct {
	buf   []byte
	isKey bool
}

func NewAccessUnitAssembler() *AccessUnitAssembler {
	return &AccessUnitAssembler{buf: make([]byte, 0, 64*1024)}
}

// Push adds a NAL unit and returns a completed Access Unit when the NAL starts
// a new one (i.e. it is an AUD and data is already pending).
func (a *AccessUnitAssembler) Push(nal NALUnit) *AccessUnit {
	var done *AccessUnit

	if nal.Type == NALUTypeAUD && len(a.buf) > 0 {
		done = a.take()
	} else if len(a.buf)+len(nal.Data) > maxAccessUnitSize {
		done = a.take()
	}

	a.buf = append(a.buf, nal.Data...)
	if nal.IsKeyframe {
		a.isKey = true
	}
	return done
}

func (a *AccessUnitAssembler) take() *AccessUnit {
	out := make([]byte, len(a.buf))
	copy(out, a.buf)
	au := &AccessUnit{Data: out, IsKeyframe: a.isKey}
	a.buf = a.buf[:0]
	a.isKey = false
	return au
}
