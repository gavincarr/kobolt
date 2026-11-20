package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gavincarr/kobolt"
)

// New (Next.js) product-page template.
//
// Kobo is migrating storefronts region by region to a Next.js page that has
// no #ratings-widget-details-wrapper gizmo config. Its data arrives as a
// React Server Components (RSC) payload streamed via inline
// <script>self.__next_f.push([1,"<chunk>"])</script> tags; concatenating the
// chunks gives newline-separated RSC rows, most of them JSON. Two objects in
// it matter:
//
//   - "priceDetail":{"listPrice":..,"displayPrice":..,"currency":..} in a
//     react-query dehydrated-state row. It streams in several seconds after
//     the visible DOM price, so callers poll for it.
//   - "itemDetails":{"metadata":{"title":..,"isbn":..},"contributors":[..]}
//     for the product itself. Related-book lists elsewhere in the payload
//     carry their own titles/authors, so metadata is only ever read from the
//     itemDetails object. (Other mentions of itemDetails are RSC reference
//     strings like "$14:props:children:0:props:itemDetails", which don't
//     match the `"itemDetails":` key.)

// errNoPriceDetail means the RSC payload doesn't (yet) contain a complete
// priceDetail object; the caller should keep polling.
var errNoPriceDetail = errors.New("priceDetail not found in RSC payload")

type rscPriceDetail struct {
	ListPrice    float64 `json:"listPrice"`
	DisplayPrice float64 `json:"displayPrice"`
	Currency     string  `json:"currency"`
}

type rscContributor struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	IsNarrator bool   `json:"isNarrator"`
}

type rscItemDetails struct {
	Metadata struct {
		Title string `json:"title"`
		ISBN  string `json:"isbn"`
	} `json:"metadata"`
	Contributors []rscContributor `json:"contributors"`
}

// rscValue returns the JSON value following the first occurrence of
// `"key":` in text. ok is false if the key is absent or its value is not yet
// complete (truncated mid-stream); err is set only for malformed JSON.
func rscValue(text, key string) (raw json.RawMessage, ok bool, err error) {
	needle := `"` + key + `":`
	i := strings.Index(text, needle)
	if i < 0 {
		return nil, false, nil
	}
	// json.Decoder stops after one value, ignoring whatever follows.
	dec := json.NewDecoder(strings.NewReader(text[i+len(needle):]))
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%s: %w", key, err)
	}
	return raw, true, nil
}

// parseRSC extracts prices from the RSC payload text into rp, and the
// product's isbn/title/author. It returns errNoPriceDetail (and leaves rp
// untouched) when priceDetail hasn't streamed in yet.
func parseRSC(text string, rp *kobolt.RegionPrice) (isbn, title, author string, err error) {
	raw, ok, err := rscValue(text, "priceDetail")
	if err != nil {
		return "", "", "", err
	}
	if !ok {
		return "", "", "", errNoPriceDetail
	}
	// Anything but an object (null, "$undefined", an RSC "$2b" reference)
	// is a placeholder rather than the real value: keep polling.
	if !bytes.HasPrefix(raw, []byte("{")) {
		return "", "", "", errNoPriceDetail
	}
	var pd rscPriceDetail
	if err := json.Unmarshal(raw, &pd); err != nil {
		return "", "", "", fmt.Errorf("priceDetail: %w", err)
	}
	rp.Price = pd.DisplayPrice
	rp.ListPrice = pd.ListPrice
	rp.Currency = pd.Currency

	// In observed pages itemDetails streams before priceDetail; if it's
	// somehow missing, keep the prices and leave the metadata empty.
	isbn, title, author, _ = rscMetadata(text)
	return isbn, title, author, nil
}

// rscMetadata returns the product's own isbn/title/author from the
// itemDetails object. ok is false if there is no (complete) itemDetails
// object with a title or isbn.
func rscMetadata(text string) (isbn, title, author string, ok bool) {
	raw, found, err := rscValue(text, "itemDetails")
	if err != nil || !found || !bytes.HasPrefix(raw, []byte("{")) {
		return "", "", "", false
	}
	var item rscItemDetails
	if err := json.Unmarshal(raw, &item); err != nil {
		return "", "", "", false
	}
	if item.Metadata.Title == "" && item.Metadata.ISBN == "" {
		return "", "", "", false
	}
	return item.Metadata.ISBN, item.Metadata.Title, rscAuthors(item.Contributors), true
}

// rscAuthors joins the names of non-narrator Author contributors with ", "
// (matching the legacy parseAuthor output).
func rscAuthors(cs []rscContributor) string {
	var names []string
	for _, c := range cs {
		if c.Type == "Author" && !c.IsNarrator && c.Name != "" {
			names = append(names, c.Name)
		}
	}
	return strings.Join(names, ", ")
}
