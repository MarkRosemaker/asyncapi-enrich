package enrich_test

import (
	"encoding/json/jsontext"
	"os"
	"strings"
	"testing"

	"github.com/MarkRosemaker/asyncapi"
	enrich "github.com/MarkRosemaker/asyncapi-enrich"
)

// pricingDataProto is the real PricingData message Yahoo Finance's pricing
// feed encodes, as .proto source — not inferred, given.
const pricingDataProto = `syntax = "proto3";

message PricingData {
  string id = 1;
  float price = 2;
  sint64 time = 3;
  string currency = 4;
  string exchange = 5;
  QuoteType quoteType = 6;
  MarketHoursType marketHours = 7;
  float changePercent = 8;
  sint64 dayVolume = 9;
  float dayHigh = 10;
  float dayLow = 11;
  float change = 12;
  string shortName = 13;
  sint64 expireDate = 14;
  float openPrice = 15;
  float previousClose = 16;
  float strikePrice = 17;
  string underlyingSymbol = 18;
  sint64 openInterest = 19;
  OptionType optionsType = 20;
  sint64 miniOption = 21;
  sint64 lastSize = 22;
  float bid = 23;
  sint64 bidSize = 24;
  float ask = 25;
  sint64 askSize = 26;
  sint64 priceHint = 27;
  sint64 vol_24hr = 28;
  sint64 volAllCurrencies = 29;
  string fromcurrency = 30;
  string lastMarket = 31;
  double circulatingSupply = 32;
  double marketcap = 33;
}

enum QuoteType {
  NONE = 0;
  ALTSYMBOL = 5;
  HEARTBEAT = 7;
  EQUITY = 8;
  INDEX = 9;
  MUTUALFUND = 11;
  MONEYMARKET = 12;
  OPTION = 13;
  CURRENCY = 14;
  WARRANT = 15;
  BOND = 17;
  FUTURE = 18;
  ETF = 20;
  COMMODITY = 23;
  ECNQUOTE = 28;
  CRYPTOCURRENCY = 41;
  INDICATOR = 42;
  INDUSTRY = 1000;
}

enum MarketHoursType {
  PRE_MARKET = 0;
  REGULAR_MARKET = 1;
  POST_MARKET = 2;
  EXTENDED_HOURS_MARKET = 3;
}

enum OptionType {
  CALL = 0;
  PUT = 1;
}
`

// pricingDocWithContentSchema builds a document whose Pricing.message is
// already documented — ContentEncoding and ContentSchema both set, the way a
// maintainer who has the real .proto would leave it — before any session is
// ever enriched into it.
func pricingDocWithContentSchema(t *testing.T, protoSource string) *asyncapi.Document {
	t.Helper()

	doc := enrich.NewDocument()
	doc.Components.Schemas = asyncapi.Schemas{
		"Pricing": &asyncapi.AnySchemaRef{Value: &asyncapi.AnySchema{Schema: &asyncapi.Schema{
			Type: asyncapi.DataTypes{asyncapi.TypeObject},
			Properties: asyncapi.Schemas{
				"message": &asyncapi.AnySchemaRef{Value: &asyncapi.AnySchema{Schema: &asyncapi.Schema{
					Type:            asyncapi.DataTypes{asyncapi.TypeString},
					ContentEncoding: "base64",
					ContentSchema: &asyncapi.AnySchemaRef{Value: &asyncapi.AnySchema{
						SchemaFormat: asyncapi.SchemaFormatProtobuf3,
						Raw:          jsontext.Value(mustJSON(t, protoSource)),
					}},
				}}},
				"type": &asyncapi.AnySchemaRef{Value: &asyncapi.AnySchema{Schema: &asyncapi.Schema{
					Type: asyncapi.DataTypes{asyncapi.TypeString},
				}}},
			},
		}}},
	}

	return doc
}

// realYahooSessions loads the actual recorded Yahoo Finance sessions this
// repo already ships as a fixture — real traffic, not synthesized for this
// test.
func realYahooSessions(t *testing.T) enrich.Sessions {
	t.Helper()

	data, err := os.ReadFile("testdata/yahoo-finance/api/sessions.json")
	if err != nil {
		t.Fatal(err)
	}

	ss, err := enrich.ParseSessions(data)
	if err != nil {
		t.Fatal(err)
	}

	return ss
}

// TestValidateProtoSchema_MatchesRealSessions checks the case a maintainer
// who pastes in the real .proto is relying on: enriching real recorded
// traffic against the schema that actually produced it does not fail.
func TestValidateProtoSchema_MatchesRealSessions(t *testing.T) {
	doc := pricingDocWithContentSchema(t, pricingDataProto)

	if err := enrich.Enrich(doc, realYahooSessions(t)); err != nil {
		t.Fatalf("enriching against the real .proto should not fail: %v", err)
	}
}

// TestValidateProtoSchema_ConflictsWithRealSessions checks the opposite: a
// .proto that declares field 1 as int32 (wire type varint) instead of the
// real string (wire type length-delimited) does not match what every
// recorded sample actually shows, and Enrich says so — which field, what was
// declared, what the wire bytes actually show, and which kinds would have
// matched.
func TestValidateProtoSchema_ConflictsWithRealSessions(t *testing.T) {
	conflicting := strings.Replace(pricingDataProto, "string id = 1;", "int32 id = 1;", 1)
	if conflicting == pricingDataProto {
		t.Fatal("test setup: replacement did not match anything in pricingDataProto")
	}

	doc := pricingDocWithContentSchema(t, conflicting)

	err := enrich.Enrich(doc, realYahooSessions(t))
	if err == nil {
		t.Fatal("expected enriching to fail: the recording does not use int32 for field 1")
	}

	msg := err.Error()

	for _, want := range []string{
		"Pricing.message",        // which field
		"field 1",                // which proto field
		`"id"`,                   // its declared name
		"int32",                  // what was declared
		"varint",                 // the wire type that declaration requires
		"length-delimited",       // the wire type actually recorded
		"bytes, message, string", // suggested kinds consistent with what was recorded
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; got:\n%s", want, msg)
		}
	}
}
