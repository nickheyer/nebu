package sources

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestModelScopeFacetsComeFromItsTaxonomyCountsAndOrganizations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/tasks":
			w.Write([]byte(`{"Code":200,"Data":{"Domains":[
				{"DomainName":"nlp","Tasks":[{"Name":"text-generation","Tasks":null}]},
				{"DomainName":"cv","Tasks":[{"Name":"vision-detection-tracking","Tasks":[{"Name":"image-object-detection"}]}]}
			]}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/dolphin/models":
			if got := string(body); got != `{"Name":"","PageNumber":1,"PageSize":1,"SortBy":"Default"}` {
				t.Errorf("counts are read from one unfiltered model, got %s", got)
			}
			w.Write([]byte(`{"Code":200,"Data":{"Model":{"TotalCount":9,"Models":[]},"FiledAgg":{
				"libraries":[{"Count":9,"Value":"gguf"},{"Count":2,"Value":"onnx"}],
				"license":[{"Count":5,"Value":"apache license 2.0"}],
				"language":[{"Count":7,"Value":"en"},{"Count":3,"Value":"null"},{"Count":1,"Value":""}],
				"model_type":[{"Count":4,"Value":"qwen3"}],
				"tags":[{"Count":6,"Value":"lora"}],
				"nexa_catalog":[{"Count":2,"Value":"life-sciences"}]
			}}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/dolphin/organizations":
			if got := string(body); got != `{"PageNumber":1,"PageSize":100,"Query":""}` {
				t.Errorf("organizations body %s", got)
			}
			w.Write([]byte(`{"Code":200,"Data":{"Suggests":[{"Name":"Qwen","FullName":"千问"},{"Name":""}],"TotalCount":2}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := testClient(t, modelscope, &v1.Source{Id: "ms", Kind: v1.SourceKind_SOURCE_KIND_MODELSCOPE, Config: map[string]string{"endpoint": srv.URL}})
	facets := map[string]*v1.Facet{}
	var order []string
	for _, f := range c.Capabilities(context.Background()).GetFacets() {
		facets[f.GetId()] = f
		order = append(order, f.GetId())
	}
	if got, want := strings.Join(order, " "), "task library license language model_type tag science publisher service relation base_model author"; got != want {
		t.Fatalf("facets\nwant %s\n got %s", want, got)
	}
	values := func(id string) string {
		var out []string
		for _, v := range facets[id].GetValues() {
			out = append(out, v.GetId()+"="+v.GetLabel()+"@"+v.GetGroup())
		}
		return strings.Join(out, " ")
	}
	for id, want := range map[string]string{
		FacetTask:        "text-generation=Text generation@nlp vision-detection-tracking=Vision detection tracking@cv vision-detection-tracking:image-object-detection=Image object detection@cv",
		FacetLibrary:     "gguf=gguf@ onnx=onnx@",
		FacetLicense:     "apache license 2.0=apache license 2.0@",
		msFacetLanguage:  "en=en@",
		msFacetModelType: "qwen3=qwen3@",
		FacetTag:         "lora=lora@",
		msFacetScience:   "life-sciences=Life sciences@",
		FacetPublisher:   "Qwen=Qwen@",
		msFacetRelation:  "finetune=Finetune@ adapter=Adapter@ quantized=Quantized@ merge=Merge@",
	} {
		if got := values(id); got != want {
			t.Fatalf("%s values\nwant %s\n got %s", id, want, got)
		}
	}
	if got := len(facets[msFacetService].GetValues()); got != len(msServices) || facets[msFacetService].GetValues()[0].GetGroup() != "Inference" {
		t.Fatalf("services %v", facets[msFacetService].GetValues())
	}
	for _, id := range []string{FacetLibrary, FacetLicense, msFacetLanguage, msFacetModelType, FacetTag, msFacetScience, FacetPublisher, FacetBaseModel} {
		if !facets[id].GetMulti() {
			t.Fatalf("%s goes several values to one criterion", id)
		}
	}
	for _, id := range []string{FacetTask, msFacetService, msFacetRelation, FacetAuthor} {
		if facets[id].GetMulti() {
			t.Fatalf("%s goes one value to a request", id)
		}
	}
	if facets[FacetBaseModel].GetInput() != v1.FacetInput_FACET_INPUT_TEXT || facets[FacetAuthor].GetInput() != v1.FacetInput_FACET_INPUT_TEXT {
		t.Fatal("base model and owner are typed")
	}
}

func TestModelScopeSendsEveryFilterAndOrder(t *testing.T) {
	var got msQuery
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = msQuery{}
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/dolphin/models" || json.NewDecoder(r.Body).Decode(&got) != nil {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		w.Write([]byte(`{"Code":200,"Data":{"Model":{"TotalCount":50,"Models":[
			{"Path":"org","Name":"q-gguf","Downloads":7,"Stars":-2,"StorageSize":1000,"Architectures":[],"ModelType":["qwen3"],
			 "BaseModel":["","org/base"],"BaseModelRelation":"quantized","Language":["en","zh"],"AigcType":"","Libraries":["gguf"],
			 "Tasks":[{"Name":"text-generation"}],"ModelInfos":{"gguf":{"architecture":"qwen3","model_size":8000000000}}},
			{"Path":"org","Name":"lora","Architectures":["Qwen3ForCausalLM"],"AigcType":"LoRA",
			 "ModelInfos":{"safetensor":{"model_size":42,"tensor_type":["BF16","F32"]}}}
		]}}}`))
	}))
	defer srv.Close()
	c := testClient(t, modelscope, &v1.Source{Id: "ms", Kind: v1.SourceKind_SOURCE_KIND_MODELSCOPE, Config: map[string]string{"endpoint": srv.URL}})
	criteria := func() string {
		data, _ := json.Marshal(struct {
			C []msCriterion
			S []msSingle
		}{got.Criterion, got.SingleCriterion})
		return string(data)
	}
	resp, err := c.Search(context.Background(), &v1.SearchRequest{Query: " qwen ", Sort: SortCreated, Ascending: true, Limit: 2, Author: "someone", Tags: []string{"chat"}, Filters: map[string]string{
		FacetTask:        "vision-detection-tracking:image-object-detection",
		FacetLibrary:     "gguf,onnx",
		FacetLicense:     "mit",
		msFacetLanguage:  "fr,ar",
		msFacetModelType: "qwen3",
		FacetTag:         "lora",
		msFacetScience:   "physics",
		FacetPublisher:   "Qwen,deepseek-ai",
		FacetBaseModel:   "org/base,org/other",
		msFacetRelation:  "quantized",
		msFacetService:   "swingDeploy",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "qwen" || got.SortBy != "GmtCreated" || got.Order != "asc" || got.PageSize != 2 || got.PageNumber != 1 {
		t.Fatalf("query %+v", got)
	}
	want := `{"C":[` +
		`{"category":"tasks","predicate":"contains","values":["vision-detection-tracking"],"sub_values":["image-object-detection"]},` +
		`{"category":"libraries","predicate":"contains","values":["gguf","onnx"]},` +
		`{"category":"license","predicate":"contains","values":["mit"]},` +
		`{"category":"languages","predicate":"contains","values":["fr","ar"]},` +
		`{"category":"model_type","predicate":"contains","values":["qwen3"]},` +
		`{"category":"tags","predicate":"contains","values":["lora"]},` +
		`{"category":"nexa_catalogs","predicate":"contains","values":["physics"]},` +
		`{"category":"organizations","predicate":"contains","values":["Qwen","deepseek-ai"]},` +
		`{"category":"organizations","predicate":"contains","values":["someone"]},` +
		`{"category":"base_model_no_ver","predicate":"contains","values":["org/base","org/other"]},` +
		`{"category":"tags","predicate":"contains","values":["chat"]}],` +
		`"S":[{"category":"base_model_relation","predicate":"equal","DateType":"string","StringValue":"quantized"},` +
		`{"category":"support_deployment","predicate":"equal","DateType":"int","IntValue":1}]}`
	if criteria() != want {
		t.Fatalf("criteria\nwant %s\n got %s", want, criteria())
	}
	if resp.GetTotal() != 50 || resp.GetNextCursor() != "2" || len(resp.GetHits()) != 2 {
		t.Fatalf("response %v", resp)
	}
	gguf, lora := resp.GetHits()[0], resp.GetHits()[1]
	if gguf.GetLikes() != 0 || gguf.GetDownloads() != 7 || gguf.GetSizeBytes() != 1000 || gguf.GetParameters() != 8e9 || gguf.GetTask() != "text-generation" || gguf.GetLibrary() != "gguf" {
		t.Fatalf("gguf hit %v", gguf)
	}
	for key, want := range map[string]string{"architecture": "qwen3", "model_type": "qwen3", "base_model": "org/base", "relation": "quantized", "languages": "en,zh"} {
		if gguf.GetExtra()[key] != want {
			t.Fatalf("gguf %s %q, want %q", key, gguf.GetExtra()[key], want)
		}
	}
	if _, ok := gguf.GetExtra()["aigc_type"]; ok {
		t.Fatal("a blank field leaves its extra out")
	}
	if lora.GetParameters() != 42 || lora.GetExtra()["architecture"] != "Qwen3ForCausalLM" || lora.GetExtra()["dtypes"] != "BF16,F32" || lora.GetExtra()["aigc_type"] != "LoRA" {
		t.Fatalf("lora hit %v", lora)
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Filters: map[string]string{FacetTask: "text-generation", msFacetService: "paiModelEvaluation"}}); err != nil {
		t.Fatal(err)
	}
	want = `{"C":[{"category":"tasks","predicate":"contains","values":["text-generation"]},{"category":"support_pai_model_gallery","predicate":"contains","values":["evaluation"]}],"S":null}`
	if got.SortBy != "Default" || got.Order != "desc" || criteria() != want {
		t.Fatalf("default order and a gallery service\nwant %s\n got %s %s %s", want, got.SortBy, got.Order, criteria())
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Filters: map[string]string{msFacetService: "teleport"}}); !errors.Is(err, ErrSource) {
		t.Fatalf("an unknown service should be refused, got %v", err)
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Sort: SortTrending, Ascending: true}); err == nil || !strings.Contains(err.Error(), "ascending") {
		t.Fatalf("the hub's own ranking runs one way, got %v", err)
	}
}
