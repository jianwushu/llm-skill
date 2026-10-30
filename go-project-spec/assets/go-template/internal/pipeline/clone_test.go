package pipeline

import "testing"

func TestRecordCloneIsDeep(t *testing.T) {
	src := &Record{
		Path:   "a.txt",
		Body:   []byte("hello"),
		Meta:   map[string]string{"lang": "zh"},
		Fields: map[string]any{"len": 5},
	}
	cp := src.Clone()

	// 修改副本不得影响原记录（并行分支共享底层数组是最隐蔽的一类 bug）
	cp.Body[0] = 'H'
	cp.Meta["lang"] = "en"
	cp.Fields["len"] = 99

	if string(src.Body) != "hello" {
		t.Fatalf("Body shared: %q", src.Body)
	}
	if src.Meta["lang"] != "zh" {
		t.Fatalf("Meta shared: %v", src.Meta)
	}
	if src.Fields["len"] != 5 {
		t.Fatalf("Fields shared: %v", src.Fields)
	}
	if cp.Path != src.Path {
		t.Fatalf("Path not copied: %q", cp.Path)
	}

	// nil 记录与空字段不应 panic
	var nilRec *Record
	if nilRec.Clone() != nil {
		t.Fatal("nil record should clone to nil")
	}
	if (&Record{}).Clone() == nil {
		t.Fatal("empty record should clone to non-nil")
	}
}
