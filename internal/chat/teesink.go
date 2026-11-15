package chat

// Tee sends one turn to two sinks — the channel the person is reading it on
// (a Telegram message being edited) and the room that records it. Each sink
// gets the optional parts it understands (thinking, the plan) and only those.
func Tee(primary, mirror TurnSink) TurnSink {
	if mirror == nil {
		return primary
	}
	return teeSink{primary, mirror}
}

type teeSink struct{ a, b TurnSink }

func (t teeSink) Write(chunk string)       { t.a.Write(chunk); t.b.Write(chunk) }
func (t teeSink) NoteTool(label string)    { t.a.NoteTool(label); t.b.NoteTool(label) }
func (t teeSink) Flush()                   { t.a.Flush(); t.b.Flush() }
func (t teeSink) SendPhoto(path, c string) { t.a.SendPhoto(path, c); t.b.SendPhoto(path, c) }
func (t teeSink) Finalize()                { t.a.Finalize(); t.b.Finalize() }

func (t teeSink) NoteThinking(chunk string) {
	for _, s := range []TurnSink{t.a, t.b} {
		if ts, ok := s.(ThinkingSink); ok {
			ts.NoteThinking(chunk)
		}
	}
}

func (t teeSink) NoteTodos(todos []Todo) {
	for _, s := range []TurnSink{t.a, t.b} {
		if ts, ok := s.(TodoSink); ok {
			ts.NoteTodos(todos)
		}
	}
}
