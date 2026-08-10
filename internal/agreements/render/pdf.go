package render

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/agreements/domain"
	aiapi "booking/go-server/shared/ai_api"

	"github.com/jung-kurt/gofpdf"
)

//go:embed assets/SpaceGrotesk-Regular.ttf
var spaceGroteskRegular []byte

//go:embed assets/SpaceGrotesk-SemiBold.ttf
var spaceGroteskSemiBold []byte

type AcceptanceEvidence struct {
	Method       domain.ConfirmationMethod
	SignerName   string
	SignaturePNG []byte
	AcceptedAt   time.Time
}

type PDFInput struct {
	BusinessName      string
	Title             string
	BookingSummary    BookingSummary
	ResolvedDocument  aiapi.DocumentSchema
	SchemaVersion     int
	RendererVersion   int
	ResolvedTermsHash string
	Acceptance        AcceptanceEvidence
}

func RenderCompletedPDF(input PDFInput) ([]byte, error) {
	return RenderCompletedPDFVersion(input.RendererVersion, input)
}

func RenderCompletedPDFVersion(version int, input PDFInput) ([]byte, error) {
	if version != RendererVersion {
		return nil, fmt.Errorf("unsupported agreement renderer version %d", version)
	}
	input.Title = normalizeResolvedText(input.Title)
	input.BusinessName = normalizeResolvedText(input.BusinessName)
	input.BookingSummary = normalizeBookingSummary(input.BookingSummary)
	input.Acceptance.SignerName = normalizeResolvedText(input.Acceptance.SignerName)
	if input.SchemaVersion != aiapi.AgreementDocumentSchemaVersion {
		return nil, fmt.Errorf("unsupported agreement schema version %d", input.SchemaVersion)
	}
	if err := validateSnapshotMetadata(input.Title, input.BookingSummary); err != nil {
		return nil, err
	}
	if err := validateAcceptanceEvidence(input.Acceptance); err != nil {
		return nil, err
	}
	wantHash, err := ResolvedTermsHash(input.Title, input.BookingSummary, input.ResolvedDocument, input.Acceptance.Method)
	if err != nil {
		return nil, err
	}
	if input.ResolvedTermsHash == "" || input.ResolvedTermsHash != wantHash {
		return nil, fmt.Errorf("resolved terms hash does not match the accepted document")
	}

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetCatalogSort(true)
	pdf.AddUTF8FontFromBytes("SpaceGrotesk", "", spaceGroteskRegular)
	pdf.AddUTF8FontFromBytes("SpaceGrotesk", "B", spaceGroteskSemiBold)
	pdf.SetTitle(input.Title, true)
	pdf.SetAuthor(defaultPDFString(input.BusinessName, "TellBook"), true)
	pdf.SetCreator("TellBook", true)
	pdf.SetCreationDate(input.Acceptance.AcceptedAt.UTC())
	pdf.SetModificationDate(input.Acceptance.AcceptedAt.UTC())
	pdf.SetMargins(18, 18, 18)
	pdf.SetAutoPageBreak(true, 20)
	pdf.SetFooterFunc(func() {
		pdf.SetY(-14)
		pdf.SetFont("SpaceGrotesk", "", 8)
		pdf.SetTextColor(112, 91, 96)
		pdf.CellFormat(0, 5, fmt.Sprintf("TellBook agreement  •  Page %d", pdf.PageNo()), "", 0, "C", false, 0, "")
	})
	pdf.AddPage()

	pdf.SetFont("SpaceGrotesk", "B", 19)
	pdf.SetTextColor(42, 19, 24)
	pdf.MultiCell(0, 9, input.Title, "", "L", false)
	pdf.Ln(2)
	renderPDFBookingSummary(pdf, input.BookingSummary)
	renderPDFDocument(pdf, input.ResolvedDocument)
	if err := renderPDFAcceptance(pdf, input.Acceptance, input.ResolvedTermsHash); err != nil {
		return nil, err
	}

	if pdf.Error() != nil {
		return nil, fmt.Errorf("prepare agreement PDF: %w", pdf.Error())
	}
	var output bytes.Buffer
	if err := pdf.Output(&output); err != nil {
		return nil, fmt.Errorf("render agreement PDF: %w", err)
	}
	return output.Bytes(), nil
}

func validateAcceptanceEvidence(evidence AcceptanceEvidence) error {
	if _, err := domain.ParseConfirmationMethod(string(evidence.Method)); err != nil {
		return err
	}
	if evidence.AcceptedAt.IsZero() {
		return fmt.Errorf("acceptance timestamp is required")
	}
	switch evidence.Method {
	case domain.ConfirmationMethodSignature:
		if evidence.SignerName == "" {
			return fmt.Errorf("signer name is required for signature acceptance")
		}
		if len(evidence.SignaturePNG) == 0 {
			return fmt.Errorf("signature PNG is required for signature acceptance")
		}
	case domain.ConfirmationMethodConfirmation:
		if evidence.SignerName != "" || len(evidence.SignaturePNG) != 0 {
			return fmt.Errorf("confirmation acceptance must not include signature evidence")
		}
	}
	return nil
}

func renderPDFBookingSummary(pdf *gofpdf.Fpdf, summary BookingSummary) {
	items := []struct {
		label string
		value string
	}{
		{label: "Service", value: summary.ServiceName},
		{label: "Date", value: summary.Date},
		{label: "Time", value: summary.Time},
		{label: "Location", value: summary.Location},
		{label: "Total", value: summary.TotalAmount},
	}
	count := visiblePDFSummaryCount(items)
	if count == 0 {
		return
	}
	pdf.SetFillColor(249, 242, 243)
	pdf.SetDrawColor(232, 213, 217)
	pdf.SetLineWidth(0.3)
	startY := pdf.GetY()
	pdf.Rect(18, startY, 174, float64(8+7*count), "DF")
	pdf.SetXY(23, startY+4)
	for _, item := range items {
		if item.value == "" {
			continue
		}
		pdf.SetFont("SpaceGrotesk", "B", 9)
		pdf.SetTextColor(99, 34, 48)
		pdf.CellFormat(25, 6, item.label, "", 0, "L", false, 0, "")
		pdf.SetFont("SpaceGrotesk", "", 9)
		pdf.SetTextColor(42, 19, 24)
		pdf.MultiCell(140, 6, item.value, "", "L", false)
		pdf.SetX(23)
	}
	pdf.SetY(startY + float64(10+7*count))
}

func visiblePDFSummaryCount(items []struct {
	label string
	value string
}) int {
	count := 0
	for _, item := range items {
		if item.value != "" {
			count++
		}
	}
	return count
}

func renderPDFDocument(pdf *gofpdf.Fpdf, document aiapi.DocumentSchema) {
	for _, block := range document.Blocks {
		switch block.Type {
		case aiapi.AgreementBlockHeading:
			pdf.Ln(2)
			size := 13.0
			if block.Level == 1 {
				size = 16
			} else if block.Level == 3 {
				size = 11
			}
			pdf.SetFont("SpaceGrotesk", "B", size)
			pdf.SetTextColor(63, 25, 33)
			pdf.MultiCell(0, size*0.48, inlinePlainText(block.Content), "", "L", false)
			pdf.Ln(1)
		case aiapi.AgreementBlockParagraph:
			pdf.SetFont("SpaceGrotesk", "", 10)
			pdf.SetTextColor(45, 37, 39)
			pdf.MultiCell(0, 5.7, inlinePlainText(block.Content), "", "L", false)
			pdf.Ln(1.5)
		case aiapi.AgreementBlockOrderedList, aiapi.AgreementBlockUnorderedList:
			pdf.SetFont("SpaceGrotesk", "", 10)
			pdf.SetTextColor(45, 37, 39)
			for index, item := range block.Items {
				prefix := "• "
				if block.Type == aiapi.AgreementBlockOrderedList {
					prefix = fmt.Sprintf("%d. ", index+1)
				}
				pdf.SetX(23)
				pdf.MultiCell(164, 5.7, prefix+inlinePlainText(item), "", "L", false)
			}
			pdf.Ln(1)
		case aiapi.AgreementBlockDivider:
			pdf.Ln(2)
			y := pdf.GetY()
			pdf.SetDrawColor(224, 207, 211)
			pdf.Line(18, y, 192, y)
			pdf.Ln(4)
		case aiapi.AgreementBlockAcceptance:
			// Acceptance evidence is rendered once after the terms.
		}
	}
}

func renderPDFAcceptance(pdf *gofpdf.Fpdf, evidence AcceptanceEvidence, termsHash string) error {
	pdf.Ln(5)
	pdf.SetDrawColor(99, 34, 48)
	pdf.Line(18, pdf.GetY(), 192, pdf.GetY())
	pdf.Ln(5)
	pdf.SetFont("SpaceGrotesk", "B", 12)
	pdf.SetTextColor(63, 25, 33)
	pdf.MultiCell(0, 6, "Customer acceptance", "", "L", false)
	pdf.SetFont("SpaceGrotesk", "", 9.5)
	pdf.SetTextColor(45, 37, 39)
	if evidence.Method == domain.ConfirmationMethodSignature {
		pdf.MultiCell(0, 5.5, "Signed by: "+evidence.SignerName, "", "L", false)
	} else {
		pdf.MultiCell(0, 5.5, "The customer confirmed and accepted these terms.", "", "L", false)
	}
	pdf.MultiCell(0, 5.5, "Accepted at: "+evidence.AcceptedAt.UTC().Format(time.RFC3339), "", "L", false)
	if evidence.Method == domain.ConfirmationMethodSignature {
		options := gofpdf.ImageOptions{ImageType: "PNG", ReadDpi: true}
		name := "customer-signature"
		if info := pdf.RegisterImageOptionsReader(name, options, bytes.NewReader(evidence.SignaturePNG)); info == nil {
			return fmt.Errorf("embed customer signature in agreement PDF")
		}
		pdf.ImageOptions(name, 18, pdf.GetY()+2, 60, 0, false, options, 0, "")
		pdf.Ln(25)
	}
	pdf.Ln(2)
	pdf.SetFont("SpaceGrotesk", "", 7.5)
	pdf.SetTextColor(112, 91, 96)
	pdf.MultiCell(0, 4.5, "Accepted terms SHA-256: "+termsHash, "", "L", false)
	return nil
}

func inlinePlainText(nodes []aiapi.AgreementInlineNode) string {
	var output strings.Builder
	for _, node := range nodes {
		output.WriteString(node.Text)
	}
	return output.String()
}

func defaultPDFString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
