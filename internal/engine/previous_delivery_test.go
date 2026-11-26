package engine

import (
	"testing"

	"kindle-sync/internal/model"
)

func TestPreviousDeliveryMatchesItsOwnDateAndState(t *testing.T) {
	const oldName = "reednd-quill.pdf"
	const newName = "reed26-quill.pdf"
	const token = "KRS-ABCDEFGHIJKLMNOP"
	const first = "2026-01-01T00:00:00Z"
	const second = "2026-02-01T00:00:00Z"
	const later = "2026-03-01T00:00:00Z"
	const parent = "SAMPLE01"
	j := model.Job{
		Paper: model.Paper{Key: parent}, State: "uncertain", NamedAt: second, OutboundName: newName,
		PreviousDeliveries: []model.Delivery{
			{State: "submitted", OutboundName: oldName, NamedAt: first},
			{State: "confirmed_received", Token: token, OutboundName: token + " Quill.pdf"},
			{State: "queued", OutboundName: "failed.pdf", NamedAt: first},
		},
	}
	r := round{jobs: map[string]model.Job{parent: j}}
	check := func(filename, created, want string) {
		t.Helper()
		got := r.deliveryParents().delivery(model.DriveFile{Name: filename, CreatedTime: created}, r.jobs)
		if got != want {
			t.Fatalf("delivery(%q, %q) = %q, want %q", filename, created, got, want)
		}
	}
	check(oldName, first, "")
	check("reednd-quill - Kindle.pdf", second, parent)
	check(newName, later, "")
	check("failed.pdf", later, "")
	check("other_"+token+" - Kindle.pdf", "", parent)
	j.State = "submitted"
	r.jobs[parent] = j
	check(newName, second, "")
	check(newName, later, parent)
	check(oldName, second, parent)

	// Two parents claiming the same old filename or token invalidate that match.
	r.jobs["SAMPLE02"] = model.Job{
		Paper: model.Paper{Key: "SAMPLE02"}, State: "submitted", OutboundName: "SAMPLE.pdf",
		PreviousDeliveries: []model.Delivery{{State: "submitted", OutboundName: oldName, NamedAt: first, Token: token}},
	}
	check(oldName, later, "")
	check("other_"+token+" - Kindle.pdf", later, "")
	check(newName, later, parent)
}

func TestPreviousDeliveryReservesFilename(t *testing.T) {
	paper := model.Paper{Key: "SAMPLE02", FirstAuthor: "Reed", Title: "Quill"}
	r := round{jobs: map[string]model.Job{"SAMPLE01": {
		Paper: model.Paper{Key: "SAMPLE01"}, OutboundName: "reed26-quill.pdf", State: "submitted",
		PreviousDeliveries: []model.Delivery{{OutboundName: "REEDnd-quill.pdf", State: "submitted"}},
	}}}
	got, err := r.allocateName(paper, "")
	if err != nil || got != "reednd-quill-sample02.pdf" {
		t.Fatalf("allocateName = %q, %v", got, err)
	}
}
