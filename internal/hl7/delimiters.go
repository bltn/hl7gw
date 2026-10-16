package hl7

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	delimiterCount                = 5
	mshFieldDelimiterIndex        = 3
	mshComponentDelimiterIndex    = 4
	mshRepetitionDelimiterIndex   = 5
	mshEscapeDelimiterIndex       = 6
	mshSubcomponentDelimiterIndex = 7

	asciiSpace        byte = 0x20
	asciiDelete       byte = 0x7F
	asciiControlCount      = int(asciiSpace) + 1
)

type delimiters struct {
	Field        string
	Component    string
	Repetition   string
	Escape       string
	Subcomponent string
}

var defaultDelimiters = delimiters{
	Field:        "|",
	Component:    "^",
	Repetition:   "~",
	Escape:       "\\",
	Subcomponent: "&",
}

func (d delimiters) encodingCharacters() string {
	return d.Component + d.Repetition + d.Escape + d.Subcomponent
}

func (d delimiters) escaper() *strings.Replacer {
	pairs := make([]string, 0, 2*(delimiterCount+asciiControlCount))
	pairs = append(pairs,
		d.Escape, d.Escape+"E"+d.Escape,
		d.Field, d.Escape+"F"+d.Escape,
		d.Component, d.Escape+"S"+d.Escape,
		d.Repetition, d.Escape+"R"+d.Escape,
		d.Subcomponent, d.Escape+"T"+d.Escape,
	)
	for b := range asciiSpace {
		pairs = append(pairs, string(b), d.hexEscape(b))
	}
	pairs = append(pairs, string(asciiDelete), d.hexEscape(asciiDelete))

	return strings.NewReplacer(pairs...)
}

func (d delimiters) hexEscape(b byte) string {
	return fmt.Sprintf("%sX%02X%s", d.Escape, b, d.Escape)
}

func parseDelimiters(mshSegment string) (delimiters, error) {
	if !strings.HasPrefix(mshSegment, mshID) {
		return delimiters{}, errors.New("hl7: first segment is not MSH")
	}
	if len(mshSegment) < len(mshID)+delimiterCount {
		return delimiters{}, errors.New("hl7: MSH header too short for delimiters")
	}
	chars := mshSegment[mshFieldDelimiterIndex : mshSubcomponentDelimiterIndex+1]
	if strings.ContainsFunc(chars, func(r rune) bool { return r >= utf8.RuneSelf }) {
		return delimiters{}, errors.New("hl7: MSH delimiters must be ASCII")
	}

	return delimiters{
		Field:        chars[0:1],
		Component:    chars[1:2],
		Repetition:   chars[2:3],
		Escape:       chars[3:4],
		Subcomponent: chars[4:5],
	}, nil
}
