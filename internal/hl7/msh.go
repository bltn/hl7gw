package hl7

import "errors"

type MSH struct {
	seg segment
}

func (h MSH) validateMessageType() error {
	if h.messageCode() == "" || h.triggerEvent() == "" {
		return errors.New("hl7: MSH-9 (message type) must include code and trigger event")
	}

	return nil
}

func correlatableMSH(seg segment) (MSH, error) {
	h := MSH{seg: seg}
	if h.ControlID() == "" {
		return MSH{}, errors.New("hl7: MSH-10 (message control ID) is missing")
	}

	return h, nil
}

func (h MSH) MessageType() string {
	if h.messageCode() == "" && h.triggerEvent() == "" {
		return ""
	}
	return h.messageCode() + "^" + h.triggerEvent()
}

func (h MSH) MissingRequiredFields() []string {
	var missing []string
	if h.processingID() == "" {
		missing = append(missing, "MSH-11")
	}
	if h.version() == "" {
		missing = append(missing, "MSH-12")
	}

	return missing
}

func (h MSH) sendingApp() string        { return h.seg.component(3, 1) }
func (h MSH) sendingFacility() string   { return h.seg.component(4, 1) }
func (h MSH) receivingApp() string      { return h.seg.component(5, 1) }
func (h MSH) receivingFacility() string { return h.seg.component(6, 1) }
func (h MSH) messageCode() string       { return h.seg.component(9, 1) }
func (h MSH) triggerEvent() string      { return h.seg.component(9, 2) }
func (h MSH) ControlID() string         { return h.seg.component(10, 1) }
func (h MSH) processingID() string      { return h.seg.component(11, 1) }
func (h MSH) version() string           { return h.seg.component(12, 1) }

func (s segment) component(fieldNum, compNum int) string {
	if fieldNum < 1 || fieldNum > len(s.Fields) {
		return ""
	}

	components := s.Fields[fieldNum-1][0]
	if compNum < 1 || compNum > len(components) {
		return ""
	}

	return components[compNum-1][0]
}
