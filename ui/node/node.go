// Copyright © 2023 Ory Corp
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"github.com/tidwall/gjson"

	"github.com/ory/kratos/schema"

	"github.com/ory/kratos/text"
	"github.com/ory/x/stringslice"
)

// swagger:enum UiNodeType
type UiNodeType string

const (
	Text     UiNodeType = "text"
	Input    UiNodeType = "input"
	Image    UiNodeType = "img"
	Anchor   UiNodeType = "a"
	Script   UiNodeType = "script"
	Division UiNodeType = "div"
)

func (t UiNodeType) String() string {
	return string(t)
}

// swagger:enum UiNodeGroup
type UiNodeGroup string

const (
	DefaultGroup         UiNodeGroup = "default"
	PasswordGroup        UiNodeGroup = "password"
	OpenIDConnectGroup   UiNodeGroup = "oidc"
	ProfileGroup         UiNodeGroup = "profile"
	LinkGroup            UiNodeGroup = "link"
	CodeGroup            UiNodeGroup = "code"
	TOTPGroup            UiNodeGroup = "totp"
	LookupGroup          UiNodeGroup = "lookup_secret"
	WebAuthnGroup        UiNodeGroup = "webauthn"
	PasskeyGroup         UiNodeGroup = "passkey"
	IdentifierFirstGroup UiNodeGroup = "identifier_first"
	CaptchaGroup         UiNodeGroup = "captcha" // Available in OEL
	SAMLGroup            UiNodeGroup = "saml"    // Available in OEL
)

func (g UiNodeGroup) String() string {
	return string(g)
}

// swagger:model uiNodes
type Nodes []*Node

// Node represents a flow's nodes
//
// Nodes are represented as HTML elements or their native UI equivalents. For example,
// a node can be an `<img>` tag, or an `<input element>` but also `some plain text`.
//
// swagger:model uiNode
type Node struct {
	// The node's type
	//
	// required: true
	Type UiNodeType `json:"type" faker:"-"`

	// Group specifies which group (e.g. password authenticator) this node belongs to.
	//
	// required: true
	Group UiNodeGroup `json:"group"`

	// The node's attributes.
	//
	// required: true
	// swagger:type uiNodeAttributes
	Attributes Attributes `json:"attributes" faker:"ui_node_attributes"`

	// The node's messages
	//
	// Contains error, validation, or other messages relevant to this node.
	//
	// required: true
	Messages text.Messages `json:"messages"`

	// Meta contains a node meta information
	//
	// This might include a label and other information that can optionally
	// be used to render UIs.
	//
	// required: true
	Meta *Meta `json:"meta"`
}

// A Node's Meta Information
//
// This might include a label and other information that can optionally
// be used to render UIs.
//
// swagger:model uiNodeMeta
type Meta struct {
	// Label represents the node's label.
	//
	// Keep in mind that these values are autogenerated and can not be changed.
	// If you wish to use other titles or labels implement that directly in
	// your UI.
	Label *text.Message `json:"label,omitempty"`
}

// Used for en/decoding the Attributes field.
type jsonRawNode struct {
	Type       UiNodeType    `json:"type"`
	Group      UiNodeGroup   `json:"group"`
	Attributes Attributes    `json:"attributes"`
	Messages   text.Messages `json:"messages"`
	Meta       *Meta         `json:"meta"`
}

func (n *Node) ID() string {
	return n.Attributes.ID()
}

func (n *Node) Reset() {
	n.Messages = nil
	n.Attributes.Reset()
}

func (n *Node) WithMetaLabel(label *text.Message) *Node {
	if n.Meta == nil {
		n.Meta = new(Meta)
	}
	n.Meta.Label = label
	return n
}

func (n *Node) GetValue() interface{} {
	return n.Attributes.GetValue()
}

func (n Nodes) Find(id string) *Node {
	for _, nn := range n {
		if nn.ID() == id {
			return nn
		}
	}

	return nil
}

func (n Nodes) Reset(exclude ...string) {
	for k, nn := range n {
		nn.Messages = nil
		if !stringslice.Has(exclude, nn.ID()) {
			nn.Reset()
		}
		n[k] = nn
	}
}

func (n Nodes) ResetNodes(reset ...string) {
	for k, nn := range n {
		if stringslice.Has(reset, nn.ID()) {
			nn.Reset()
		}
		n[k] = nn
	}
}

func (n Nodes) ResetNodesWithPrefix(prefix string) {
	for k := range n {
		if strings.HasPrefix(n[k].ID(), prefix) {
			n[k].Reset()
		}
	}
}

func getStringSliceIndexOf(needle []string, haystack string) int {
	for k := range needle {
		if needle[k] == haystack {
			return k
		}
	}
	return -1
}

type sortOptions struct {
	orderByGroups     []string
	schemaRef         string
	keysInOrder       []string
	keysInOrderAppend []string
	keysInOrderPost   func([]string) []string
}

type SortOption func(*sortOptions)

func SortByGroups(orderByGroups []UiNodeGroup) func(*sortOptions) {
	return func(options *sortOptions) {
		options.orderByGroups = make([]string, len(orderByGroups))
		for k := range orderByGroups {
			options.orderByGroups[k] = string(orderByGroups[k])
		}
	}
}

func SortBySchema(schemaRef string) func(*sortOptions) {
	return func(options *sortOptions) {
		options.schemaRef = schemaRef
	}
}

func SortUseOrder(keysInOrder []string) func(*sortOptions) {
	return func(options *sortOptions) {
		options.keysInOrder = keysInOrder
	}
}

func SortUseOrderAppend(keysInOrder []string) func(*sortOptions) {
	return func(options *sortOptions) {
		options.keysInOrderAppend = keysInOrder
	}
}

func SortUpdateOrder(f func([]string) []string) func(*sortOptions) {
	return func(options *sortOptions) {
		options.keysInOrderPost = f
	}
}

func (n Nodes) SortBySchema(ctx context.Context, opts ...SortOption) error {
	var o sortOptions
	for _, f := range opts {
		f(&o)
	}

	if o.schemaRef != "" {
		schemaKeys, err := schema.GetKeysInOrder(ctx, o.schemaRef)
		if err != nil {
			return err
		}

		o.keysInOrder = append(o.keysInOrder, schemaKeys...)
	}

	if o.keysInOrderPost != nil {
		o.keysInOrder = o.keysInOrderPost(o.keysInOrder)
	}

	o.keysInOrder = append(o.keysInOrder, o.keysInOrderAppend...)

	getKeyPosition := func(node *Node) int {
		lastPrefix := len(o.keysInOrder)

		// Method should always be the last element in the list
		if node.Attributes.ID() == "method" {
			return len(n) + len(o.keysInOrder) + 1
		}

		for i, n := range o.keysInOrder {
			if strings.HasPrefix(node.ID(), n) {
				return i
			}
		}

		return lastPrefix
	}

	if len(o.orderByGroups) > 0 {
		// Sort by groups so that default is in front, then oidc, password, ...
		sort.Slice(n, func(i, j int) bool {
			a := string(n[i].Group)
			b := string(n[j].Group)
			return getStringSliceIndexOf(o.orderByGroups, a) < getStringSliceIndexOf(o.orderByGroups, b)
		})
	}

	sort.SliceStable(n, func(i, j int) bool {
		a := n[i]
		b := n[j]

		if a.Group == b.Group {
			pa, pb := getKeyPosition(a), getKeyPosition(b)
			if pa < pb {
				return true
			} else if pa > pb {
				return false
			}

			return fmt.Sprintf("%v", a.GetValue()) < fmt.Sprintf("%v", b.GetValue())
		}

		return false
	})

	return nil
}

// Remove removes one or more nodes by their IDs.
func (n *Nodes) Remove(ids ...string) {
	if n == nil {
		return
	}

	var r Nodes
	for k, v := range *n {
		var found bool
		for _, needle := range ids {
			if (*n)[k].ID() == needle {
				found = true
				break
			}
		}
		if !found {
			r = append(r, v)
		}
	}
	*n = r
}

// Upsert updates or appends a node.
func (n *Nodes) Upsert(node *Node) {
	if n == nil {
		*n = append(*n, node)
		return
	}

	for i := range *n {
		if (*n)[i].ID() == node.ID() {
			(*n)[i] = node
			return
		}
	}

	*n = append(*n, node)
}

// SetValueAttribute sets a node's attribute's value or returns false if no node is found.
func (n *Nodes) SetValueAttribute(id string, value interface{}) bool {
	for i := range *n {
		if (*n)[i].ID() == id {
			(*n)[i].Attributes.SetValue(value)
			return true
		}
	}
	return false
}

// Append appends a node.
func (n *Nodes) Append(node *Node) {
	*n = append(*n, node)
}

func (n *Nodes) RemoveMatching(node *Node) {
	if n == nil {
		return
	}

	var r Nodes
	for k, v := range *n {
		if !(*n)[k].Matches(node) {
			r = append(r, v)
		}
	}

	*n = r
}

func (n *Node) Matches(needle *Node) bool {
	if len(needle.ID()) > 0 && n.ID() != needle.ID() {
		return false
	}

	if needle.Type != "" && n.Type != needle.Type {
		return false
	}

	if needle.Group != "" && n.Group != needle.Group {
		return false
	}

	return n.Attributes.Matches(needle.Attributes)
}

func (n *Node) UnmarshalJSON(data []byte) error {
	var attr Attributes
	switch t := gjson.GetBytes(data, "type").String(); UiNodeType(t) {
	case Text:
		attr = &TextAttributes{
			NodeType: Text,
		}
	case Input:
		attr = &InputAttributes{
			NodeType: Input,
		}
	case Anchor:
		attr = &AnchorAttributes{
			NodeType: Anchor,
		}
	case Image:
		attr = &ImageAttributes{
			NodeType: Image,
		}
	case Script:
		attr = &ScriptAttributes{
			NodeType: Script,
		}
	case Division:
		attr = &DivisionAttributes{
			NodeType: Division,
		}
	default:
		return fmt.Errorf("unexpected node type: %s", t)
	}

	var d jsonRawNode
	d.Attributes = attr
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&d); err != nil {
		return err
	}

	*n = Node(d)

	if n.Meta == nil {
		n.Meta = new(Meta)
	}

	return nil
}

func (n *Node) MarshalJSON() ([]byte, error) {
	var t UiNodeType
	if n.Attributes != nil {
		switch attr := n.Attributes.(type) {
		case *TextAttributes:
			t = Text
			attr.NodeType = Text
		case *InputAttributes:
			t = Input
			attr.NodeType = Input
		case *AnchorAttributes:
			t = Anchor
			attr.NodeType = Anchor
		case *ImageAttributes:
			t = Image
			attr.NodeType = Image
		case *ScriptAttributes:
			t = Script
			attr.NodeType = Script
		default:
			return nil, errors.WithStack(fmt.Errorf("unknown node type: %T", n.Attributes))
		}
	}

	if n.Type == "" {
		n.Type = t
	} else if n.Type != t {
		return nil, errors.WithStack(fmt.Errorf("node type and node attributes mismatch: %T != %s", n.Attributes, n.Type))
	}

	if n.Meta == nil {
		n.Meta = new(Meta)
	}

	return json.Marshal((*jsonRawNode)(n))
}
