package catalog

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestBrokenSpecificationsFailComposition(t *testing.T) {
	good := func() *Service {
		s := *syntheticSpec()
		s.Resources = []*Resource{cloneResource(syntheticSpec().Resources[0])}
		s.Ops = append([]Op(nil), syntheticSpec().Ops...)
		return &s
	}
	cases := map[string]func(*Service){
		"no title":          func(s *Service) { s.Title = "" },
		"duplicate kind":    func(s *Service) { s.Resources = append(s.Resources, cloneResource(s.Resources[0])) },
		"missing shape":     func(s *Service) { s.Resources[0].Shape = "Nope" },
		"scalar shape":      func(s *Service) { s.Resources[0].Shape = "Text" },
		"missing summary":   func(s *Service) { s.Resources[0].Summary = "Nope" },
		"unknown parent":    func(s *Service) { s.Resources[0].Parent = "nope" },
		"bad arn":           func(s *Service) { s.Resources[0].ARN = "widget/{id}" },
		"no identity":       func(s *Service) { r := s.Resources[0]; r.ARNMember, r.NameMember, r.IDMember = "", "", "" },
		"bad generator":     func(s *Service) { s.Resources[0].ID = "alnum:x" },
		"unknown generator": func(s *Service) { s.Resources[0].ID = "serial" },
		"unofficial op":     func(s *Service) { s.Ops = append(s.Ops, Op{Name: "Teleport", Kind: Read}) },
		"duplicate op":      func(s *Service) { s.Ops = append(s.Ops, s.Ops[0]) },
		"unknown resource":  func(s *Service) { s.Ops[0].Resource = "nope" },
		"unknown kind":      func(s *Service) { s.Ops[0].Kind = "destroy" },
		"missing lookup":    func(s *Service) { s.Ops[1].Lookup = "" },
		"bad lookup member": func(s *Service) { s.Ops[1].Lookup = "Nope" },
		"unbound official":  func(s *Service) { s.Ops = s.Ops[:4] },
		"child needs parent": func(s *Service) {
			s.Resources = append(s.Resources, &Resource{Kind: "part", Shape: "Widget", Parent: "widget", ID: "name", ARN: "arn:{partition}:x", NameMember: "Name"})
			s.Ops[0].Resource = "part"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := good()
			mutate(spec)
			if _, err := bindModel(spec, syntheticModel()); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
	if _, err := bindModel(good(), syntheticModel()); err != nil {
		t.Fatalf("baseline rejected: %v", err)
	}
	h := newHarness(t, ecr)
	protocol := syntheticModel()
	protocol.Metadata.Protocol = "query"
	b, err := bindModel(syntheticSpec(), protocol)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err == nil {
		t.Fatal("an unsupported protocol was registered")
	}
	if _, err := New(h.db, Config{}, nil); err == nil {
		t.Fatal("missing configuration was accepted")
	}
	if _, err := New(h.db, Config{Regions: []string{"us-east-1"}, Account: "000000000000", DefaultPrincipal: "a", Authorize: h.policy.authorize}, []*Service{ecr, ecr}); err == nil {
		t.Fatal("duplicate service was accepted")
	}
	if _, err := New(h.db, Config{Regions: []string{"us-east-1"}, Account: "000000000000", DefaultPrincipal: "a", Authorize: h.policy.authorize}, []*Service{{Model: "not-embedded"}}); err == nil {
		t.Fatal("missing model was accepted")
	}
	if _, err := newStore(nil); err == nil {
		t.Fatal("nil database accepted")
	}
	if _, err := loadModel("../engine"); err == nil {
		t.Fatal("non-model path accepted")
	}
}

func cloneResource(r *Resource) *Resource { c := *r; return &c }

func TestModelsWithoutProvenanceOrShapesAreRejected(t *testing.T) {
	model := syntheticModel()
	model.Shapes["WidgetOutput"].Members["Ghost"] = Member{Shape: "Missing"}
	if err := validateModel(model); err == nil {
		t.Fatal("dangling member shape accepted")
	}
	model = syntheticModel()
	model.Operations["GetWidget"].Output = "Missing"
	if err := validateModel(model); err == nil {
		t.Fatal("dangling operation shape accepted")
	}
	model = syntheticModel()
	model.Source.SHA256 = ""
	if err := validateModel(model); err == nil {
		t.Fatal("missing provenance accepted")
	}
}

func inject(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStorageFailuresAreReportedWithoutPartialState(t *testing.T) {
	h := syntheticHarness(t)
	created := h.json1("synthetic", "CreateWidget", map[string]any{"Name": "keep"})
	id := str(created.body["WidgetId"])
	inject(t, h.db, "CREATE TRIGGER fail_update BEFORE UPDATE ON aws_catalog_resources BEGIN SELECT RAISE(ABORT, 'injected'); END")
	expect(t, h.json1("synthetic", "UpdateWidget", map[string]any{"WidgetId": id, "Size": 2}), 500, "InternalFailure")
	inject(t, h.db, "DROP TRIGGER fail_update", "CREATE TRIGGER fail_delete BEFORE DELETE ON aws_catalog_resources BEGIN SELECT RAISE(ABORT, 'injected'); END")
	expect(t, h.json1("synthetic", "DeleteWidget", map[string]any{"WidgetId": id}), 500, "InternalFailure")
	inject(t, h.db, "DROP TRIGGER fail_delete", "CREATE TRIGGER fail_insert BEFORE INSERT ON aws_catalog_resources BEGIN SELECT RAISE(ABORT, 'injected'); END")
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "lost"}), 500, "InternalFailure")
	inject(t, h.db, "DROP TRIGGER fail_insert", "CREATE TRIGGER skip_sequence BEFORE UPDATE ON aws_catalog_sequence BEGIN SELECT RAISE(IGNORE); END")
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "lost"}), 500, "InternalFailure")
	inject(t, h.db, "DROP TRIGGER skip_sequence", "CREATE TRIGGER fail_token BEFORE INSERT ON aws_catalog_idempotency BEGIN SELECT RAISE(ABORT, 'injected'); END")
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "lost", "ClientToken": "t"}), 500, "InternalFailure")
	inject(t, h.db, "DROP TRIGGER fail_token")
	var names []string
	rows, _ := h.db.Query("SELECT name FROM aws_catalog_resources")
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, n)
	}
	_ = rows.Close()
	if strings.Join(names, ",") != "keep" {
		t.Fatalf("failed writes left partial state: %v", names)
	}
	inject(t, h.db, "UPDATE aws_catalog_resources SET document='not json'")
	expect(t, h.json1("synthetic", "GetWidget", map[string]any{"WidgetId": id}), 500, "InternalFailure")
	expect(t, h.json1("synthetic", "ListWidgets", map[string]any{}), 500, "InternalFailure")
	inject(t, h.db, "UPDATE aws_catalog_resources SET document='{}', tags='nope'")
	expect(t, h.json1("synthetic", "GetWidget", map[string]any{"WidgetId": id}), 500, "InternalFailure")
	inject(t, h.db, "DROP TABLE aws_catalog_idempotency")
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "x", "ClientToken": "t"}), 500, "InternalFailure")
	inject(t, h.db, "DROP TABLE aws_catalog_sequence")
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "x"}), 500, "InternalFailure")
	inject(t, h.db, "DROP TABLE aws_catalog_resources")
	for _, op := range []string{"GetWidget", "DeleteWidget", "UpdateWidget"} {
		expect(t, h.json1("synthetic", op, map[string]any{"WidgetId": id}), 500, "InternalFailure")
	}
	expect(t, h.json1("synthetic", "ListWidgets", map[string]any{}), 500, "InternalFailure")
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "x"}), 500, "InternalFailure")
	if _, err := newStore(h.db); err != nil {
		t.Fatalf("store recreation: %v", err)
	}
	var f *fault
	if !errors.As(error(&fault{kind: "validation", message: "m"}), &f) || f.Error() != "m" {
		t.Fatal("fault must be an error")
	}
}

func TestBudgetsBoundStoredMetadataAndTokens(t *testing.T) {
	h := syntheticHarness(t)
	inject(t, h.db, "INSERT INTO aws_catalog_resources VALUES('other','us-east-1','k','','big','big','arn:big', '{\"a\":\"' || hex(zeroblob(17000000)) || '\"}', '{}', 0, 1)")
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "over"}), 400, "LimitExceededException")
	inject(t, h.db, "DELETE FROM aws_catalog_resources")
	inject(t, h.db, "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 10000) INSERT INTO aws_catalog_idempotency SELECT 'x','us-east-1','op',i,'f','arn',9999999999 FROM n")
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "token", "ClientToken": "fresh"}), 400, "LimitExceededException")
	huge := strings.Repeat("x", maxDocumentBytes)
	b := h.binding("synthetic")
	q := &request{e: h.engine, b: b}
	if _, _, err := encodeRow(row{Document: map[string]any{"Meta": huge}}); err == nil {
		t.Fatal("oversized document accepted")
	}
	_ = q
}

func TestParentsCascadeAndAmbiguousNames(t *testing.T) {
	spec := syntheticSpec()
	parent := spec.Resources[0]
	parent.Cascade = true
	child := &Resource{Kind: "part", Shape: "Widget", Parent: "widget", ParentMember: "WidgetId", ID: "uuid", ARN: "arn:{partition}:synthetic:{region}:{account}:widget/{parent}/part/{id}/{parentName}",
		ARNMember: "WidgetArn", NameMember: "Name", Generate: map[string]any{"WidgetId": "$parentId", "Weight": 1.5, "Count": "$name", "Meta": "$unknown", "Created": "$now", "Note": "$parentArn", "Data": "$parentName", "Choice": "$region"}}
	spec.Resources = append(spec.Resources, child)
	model := syntheticModel()
	model.Routes["CreatePart"], model.Routes["ListParts"] = Route{}, Route{}
	model.Operations["CreatePart"] = &Operation{Name: "CreatePart", Input: "CreatePartInput", Output: "WidgetOutput"}
	model.Operations["ListParts"] = &Operation{Name: "ListParts", Input: "IDInput", Output: "ListOutput"}
	model.Shapes["CreatePartInput"] = &Shape{Type: "structure", Required: []string{"WidgetId", "Name"}, Members: map[string]Member{"WidgetId": {Shape: "Text"}, "Name": {Shape: "Name"}}}
	spec.Ops = append(spec.Ops, Op{Name: "CreatePart", Kind: Create, Resource: "part", Parent: "WidgetId"}, Op{Name: "ListParts", Kind: List, Resource: "part", Parent: "WidgetId"})
	h := newHarness(t, ecr)
	b, err := bindModel(spec, model)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.add(b); err != nil {
		t.Fatal(err)
	}
	w := h.json1("synthetic", "CreateWidget", map[string]any{"Name": "dup"})
	id := str(w.body["WidgetId"])
	h.json1("synthetic", "CreateWidget", map[string]any{"Name": "dup"})
	expect(t, h.json1("synthetic", "GetWidget", map[string]any{"WidgetId": "dup"}), 400, "ValidationException")
	part := h.json1("synthetic", "CreatePart", map[string]any{"WidgetId": id, "Name": "p1"})
	expect(t, part, 200, "")
	if !strings.Contains(str(part.body["WidgetArn"]), "/part/") || !strings.HasSuffix(str(part.body["WidgetArn"]), "/dup") {
		t.Fatalf("child ARN %v", part.body)
	}
	expect(t, h.json1("synthetic", "CreatePart", map[string]any{"WidgetId": "w-missing", "Name": "p"}), 400, "ResourceNotFoundException")
	if parts := h.json1("synthetic", "ListParts", map[string]any{"WidgetId": id}).body["Widgets"].([]any); len(parts) != 1 {
		t.Fatalf("parts %v", parts)
	}
	expect(t, h.json1("synthetic", "DeleteWidget", map[string]any{"WidgetId": id}), 200, "")
	var n int
	_ = h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources WHERE kind='part'").Scan(&n)
	if n != 0 {
		t.Fatalf("cascade left %d children", n)
	}
	inject(t, h.db, "CREATE TRIGGER fail_children BEFORE DELETE ON aws_catalog_resources WHEN old.kind='part' BEGIN SELECT RAISE(ABORT, 'x'); END")
	w2 := h.json1("synthetic", "CreateWidget", map[string]any{"Name": "solo"})
	h.json1("synthetic", "CreatePart", map[string]any{"WidgetId": str(w2.body["WidgetId"]), "Name": "p"})
	expect(t, h.json1("synthetic", "DeleteWidget", map[string]any{"WidgetId": str(w2.body["WidgetId"])}), 500, "InternalFailure")
}
