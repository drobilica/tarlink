package manifest

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	maxSVGBytes    = 4 << 20
	maxSVGTokens   = 100_000
	maxSVGElements = 20_000
	maxSVGAttrSize = 64 << 10
	maxSVGDepth    = 128
	svgNamespace   = "http://www.w3.org/2000/svg"
	xlinkNamespace = "http://www.w3.org/1999/xlink"
)

var (
	staticSVGElements = map[string]bool{
		"svg": true, "g": true, "defs": true, "title": true, "desc": true,
		"path": true, "rect": true, "circle": true, "ellipse": true,
		"line": true, "polyline": true, "polygon": true,
		"linearGradient": true, "radialGradient": true, "stop": true,
		"text": true, "tspan": true,
	}
	staticSVGAttributes = map[string]bool{
		"id": true, "version": true, "baseProfile": true, "viewBox": true,
		"preserveAspectRatio": true, "width": true, "height": true,
		"x": true, "y": true, "cx": true, "cy": true, "r": true,
		"rx": true, "ry": true, "x1": true, "y1": true, "x2": true,
		"y2": true, "d": true, "points": true, "transform": true,
		"fill": true, "fill-rule": true, "fill-opacity": true,
		"stroke": true, "stroke-width": true, "stroke-linecap": true,
		"stroke-linejoin": true, "stroke-miterlimit": true,
		"stroke-dasharray": true, "stroke-dashoffset": true,
		"stroke-opacity": true, "opacity": true, "color": true,
		"gradientUnits": true, "gradientTransform": true,
		"spreadMethod": true, "offset": true, "stop-color": true,
		"stop-opacity": true, "pathLength": true, "vector-effect": true,
		"shape-rendering": true, "paint-order": true,
		"font-family": true, "font-size": true, "font-style": true,
		"font-weight": true, "text-anchor": true, "dominant-baseline": true,
	}
	localPaintReference  = regexp.MustCompile(`^url\(\s*#[A-Za-z_][A-Za-z0-9_.:-]*\s*\)$`)
	staticXMLDeclaration = regexp.MustCompile(`^\s*version\s*=\s*(?:"1\.0"|'1\.0')(?:\s+encoding\s*=\s*(?:"(?i:UTF-8)"|'(?i:UTF-8)'))?(?:\s+standalone\s*=\s*(?:"(?:yes|no)"|'(?:yes|no)'))?\s*$`)
)

// ValidateSVG accepts only bounded, well-formed SVG XML using a deliberately
// small static-artwork subset. It rejects DTDs, processing instructions other
// than an optional XML declaration, active/foreign elements, event handlers,
// stylesheets, and non-local resource references. Accepted bytes are not
// rewritten.
func ValidateSVG(data []byte) error {
	if len(data) == 0 || len(data) > maxSVGBytes {
		return fmt.Errorf("SVG icon size must be between 1 and %d bytes", maxSVGBytes)
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	depth := 0
	tokens := 0
	elements := 0
	seenRoot := false
	seenXMLDeclaration := false
	seenToken := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("SVG icon is not well-formed XML: %w", err)
		}
		tokens++
		if tokens > maxSVGTokens {
			return errors.New("SVG icon exceeds the XML token limit")
		}
		switch value := token.(type) {
		case xml.ProcInst:
			if value.Target != "xml" || seenToken || seenRoot || seenXMLDeclaration {
				return errors.New("SVG icon contains a processing instruction")
			}
			if !staticXMLDeclaration.Match(value.Inst) {
				return errors.New("SVG icon has an invalid or unsupported XML declaration")
			}
			seenXMLDeclaration = true
		case xml.Directive:
			return errors.New("SVG icon directives and DOCTYPE declarations are not allowed")
		case xml.StartElement:
			seenToken = true
			elements++
			if elements > maxSVGElements {
				return errors.New("SVG icon exceeds the XML element limit")
			}
			if value.Name.Space != svgNamespace || !staticSVGElements[value.Name.Local] {
				return fmt.Errorf("SVG icon contains unsupported element %q", value.Name.Local)
			}
			if !seenRoot {
				if depth != 0 || value.Name.Local != "svg" {
					return errors.New("SVG icon root element must be svg in the SVG namespace")
				}
				seenRoot = true
			} else if value.Name.Local == "svg" {
				return errors.New("SVG icon cannot contain a nested svg element")
			}
			if len(value.Attr) > 64 {
				return errors.New("SVG icon element exceeds the attribute limit")
			}
			for _, attribute := range value.Attr {
				if len(attribute.Value) > maxSVGAttrSize {
					return fmt.Errorf("SVG icon attribute %q exceeds the value limit", attribute.Name.Local)
				}
				if attribute.Name.Space == "xmlns" || attribute.Name.Local == "xmlns" {
					if attribute.Value != svgNamespace && attribute.Value != xlinkNamespace {
						return errors.New("SVG icon declares an unsupported XML namespace")
					}
					continue
				}
				name := attribute.Name.Local
				if attribute.Name.Space != "" || strings.HasPrefix(strings.ToLower(name), "on") || !staticSVGAttributes[name] {
					return fmt.Errorf("SVG icon contains unsupported or active attribute %q", name)
				}
				lower := strings.ToLower(attribute.Value)
				if strings.Contains(lower, "javascript:") || strings.Contains(lower, "data:") ||
					strings.Contains(lower, "http:") || strings.Contains(lower, "https:") ||
					strings.Contains(lower, "file:") {
					return fmt.Errorf("SVG icon attribute %q contains an external or active reference", name)
				}
				if strings.Contains(lower, "url") && (name != "fill" && name != "stroke" || !localPaintReference.MatchString(strings.TrimSpace(attribute.Value))) {
					return fmt.Errorf("SVG icon attribute %q contains a non-local resource reference", name)
				}
			}
			depth++
			if depth > maxSVGDepth {
				return errors.New("SVG icon exceeds the XML nesting limit")
			}
		case xml.EndElement:
			seenToken = true
			depth--
			if depth < 0 {
				return errors.New("SVG icon has invalid XML nesting")
			}
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(value)) != 0 {
				return errors.New("SVG icon contains text outside its root element")
			}
			seenToken = true
		case xml.Comment:
			seenToken = true
		}
	}
	if !seenRoot || depth != 0 {
		return errors.New("SVG icon must contain one complete svg root element")
	}
	return nil
}
