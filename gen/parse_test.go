package gen

import "testing"

func TestUnmarshalBody(t *testing.T) {
	for _, tt := range []struct {
		body string
		ok   bool
	}{
		{`{"AssetTag":"a"}`, true},
		{" {\"AssetTag\":\"a\"} \n", true},
		{`{}`, true},
		{`{} garbage`, false},
		{`{}{}`, false},
		{`{"AssetTag":"a"`, false},
		{`[]`, false},
		{`{"AssetTag":1}`, false},
	} {
		var p ChassisPatch
		if err := p.UnmarshalJSON([]byte(tt.body)); (err == nil) != tt.ok {
			t.Errorf("ChassisPatch %q: err = %v, want ok = %v", tt.body, err, tt.ok)
		}
	}
	for _, tt := range []struct {
		body string
		ok   bool
	}{
		{`{"Targets":[{"@odata.id":"/redfish/v1/UpdateService/FirmwareInventory/a"}]}`, true},
		{`{"Targets":[]}`, true},
		{`{"Targets":[{}]}`, false},
		{`{"Targets":[{"@odata.id":""}]}`, false},
		{`{"Targets":[{"Other":1}]}`, false},
	} {
		var p UpdateServiceActivateParameters
		if err := p.UnmarshalJSON([]byte(tt.body)); (err == nil) != tt.ok {
			t.Errorf("UpdateServiceActivateParameters %q: err = %v, want ok = %v", tt.body, err, tt.ok)
		}
	}
}
