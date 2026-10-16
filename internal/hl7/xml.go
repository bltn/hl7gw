package hl7

import (
	"encoding/xml"
	"regexp"
	"strconv"
)

type xmlNode struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
	Nodes   []xmlNode
}

var emptyElement = regexp.MustCompile(`<([\w.]+)></[\w.]+>`)

func XML(m Message) string {
	root := xmlNode{XMLName: xml.Name{Local: "HL7Message"}}
	root.Nodes = make([]xmlNode, 0, len(m.segments)+1)
	root.Nodes = append(root.Nodes, segmentNode(m.MSH.seg))
	for _, seg := range m.segments {
		root.Nodes = append(root.Nodes, segmentNode(seg))
	}

	out, _ := xml.MarshalIndent(root, "", "    ")

	return emptyElement.ReplaceAllString(string(out), "<$1/>") + "\n"
}

func segmentNode(s segment) xmlNode {
	n := xmlNode{XMLName: xml.Name{Local: s.ID}, Nodes: make([]xmlNode, 0, len(s.Fields))}
	for i, field := range s.Fields {
		name := childName(s.ID, i)
		if s.ID == mshID && i < 2 {
			n.Nodes = append(n.Nodes, leafNode(name, field[0][0][0]))
			continue
		}
		for _, rep := range field {
			n.Nodes = append(n.Nodes, repetitionNode(rep, name))
		}
	}

	return n
}

func repetitionNode(rep repetition, name string) xmlNode {
	if len(rep) == 1 && len(rep[0]) == 1 && rep[0][0] == "" {
		return leafNode(name, "")
	}

	n := xmlNode{XMLName: xml.Name{Local: name}, Nodes: make([]xmlNode, len(rep))}
	for c, comp := range rep {
		n.Nodes[c] = componentNode(comp, childName(name, c))
	}

	return n
}

func componentNode(comp component, name string) xmlNode {
	if len(comp) == 1 {
		return leafNode(name, comp[0])
	}

	n := xmlNode{XMLName: xml.Name{Local: name}, Nodes: make([]xmlNode, len(comp))}
	for s, sub := range comp {
		n.Nodes[s] = leafNode(childName(name, s), sub)
	}

	return n
}

func leafNode(name, value string) xmlNode {
	return xmlNode{XMLName: xml.Name{Local: name}, Value: value}
}

func childName(parent string, index int) string {
	return parent + "." + strconv.Itoa(index+1)
}
