package main

import "testing"

func TestPrinterProtocolSettingsRoundTrip(t *testing.T) {
	spoolman := newHistoryTestSpoolmanServer()
	defer spoolman.close()
	bridge := newTestBridge(t, spoolman.server.URL)

	want := PrinterConfig{
		Name:                 "Secure printer",
		Model:                ModelCoreOne,
		IPAddress:            "https://printer.local",
		APIKey:               "api-secret",
		PrusaLinkUsername:    "maker",
		PrusaLinkPassword:    "digest-secret",
		PrusaLinkCustomCAPEM: "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		Toolheads:            1,
	}
	if err := bridge.SavePrinterConfig("secure", want); err != nil {
		t.Fatalf("SavePrinterConfig() error = %v", err)
	}
	configs, err := bridge.GetAllPrinterConfigs()
	if err != nil {
		t.Fatalf("GetAllPrinterConfigs() error = %v", err)
	}
	got := configs["secure"]
	if got.IPAddress != want.IPAddress || got.APIKey != want.APIKey || got.PrusaLinkUsername != want.PrusaLinkUsername || got.PrusaLinkPassword != want.PrusaLinkPassword || got.PrusaLinkCustomCAPEM != want.PrusaLinkCustomCAPEM {
		t.Fatalf("protocol settings = %+v, want %+v", got, want)
	}
}

func TestValidateAddressAcceptsPrusaLinkURLsAndRejectsEmbeddedCredentials(t *testing.T) {
	if err := validateAddress("https://printer.local"); err != nil {
		t.Fatalf("validateAddress(HTTPS) error = %v", err)
	}
	if err := validateAddress("http://[fd00::1]:8080"); err != nil {
		t.Fatalf("validateAddress(IPv6 URL) error = %v", err)
	}
	if err := validateAddress("https://user:secret@printer.local"); err == nil {
		t.Fatal("validateAddress() accepted embedded credentials")
	}
	if err := validateAddress("ftp://printer.local"); err == nil {
		t.Fatal("validateAddress() accepted non-HTTP scheme")
	}
}
