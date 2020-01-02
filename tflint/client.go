package tflint

import (
	"fmt"
	"log"
	"net"
	"net/rpc"

	hcl "github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/gocty"
)

// Client is an RPC client for plugins to query the host process for Terraform configurations
// Actually, it is an RPC client, but its details are hidden on the plugin side because it satisfies the Runner interface
type Client struct {
	rpcClient *rpc.Client
}

// NewClient returns a new Client
func NewClient(conn net.Conn) *Client {
	return &Client{rpcClient: rpc.NewClient(conn)}
}

// AttributesRequest is the interface used to communicate via RPC.
type AttributesRequest struct {
	Resource      string
	AttributeName string
}

// WalkResourceAttributes queries the host process, receives a list of attributes that match the conditions,
// and passes each to the walker function.
func (c *Client) WalkResourceAttributes(resource, attributeName string, walker func(*hcl.Attribute) error) error {
	var resp hcl.Attributes
	if err := c.rpcClient.Call("Plugin.Attributes", AttributesRequest{Resource: resource, AttributeName: attributeName}, &resp); err != nil {
		return err
	}

	for _, attribute := range resp {
		if err := walker(attribute); err != nil {
			return err
		}
	}

	return nil
}

// EvaluateExpr queries the host process for the result of evaluating the value of the passed expression
// and reflects it as the value of the second argument based on that.
func (c *Client) EvaluateExpr(expr hcl.Expression, ret interface{}) error {
	var val cty.Value
	var err error

	if err := c.rpcClient.Call("Plugin.EvalExpr", &expr, &val); err != nil {
		return err
	}

	switch ret.(type) {
	case *string:
		val, err = convert.Convert(val, cty.String)
	case *int:
		val, err = convert.Convert(val, cty.Number)
	case *[]string:
		val, err = convert.Convert(val, cty.List(cty.String))
	case *[]int:
		val, err = convert.Convert(val, cty.List(cty.Number))
	case *map[string]string:
		val, err = convert.Convert(val, cty.Map(cty.String))
	case *map[string]int:
		val, err = convert.Convert(val, cty.Map(cty.Number))
	}

	if err != nil {
		err := &Error{
			Code:  TypeConversionError,
			Level: ErrorLevel,
			Message: fmt.Sprintf(
				"Invalid type expression in %s:%d",
				expr.Range().Filename,
				expr.Range().Start.Line,
			),
			Cause: err,
		}
		log.Printf("[ERROR] %s", err)
		return err
	}

	err = gocty.FromCtyValue(val, ret)
	if err != nil {
		err := &Error{
			Code:  TypeMismatchError,
			Level: ErrorLevel,
			Message: fmt.Sprintf(
				"Invalid type expression in %s:%d",
				expr.Range().Filename,
				expr.Range().Start.Line,
			),
			Cause: err,
		}
		log.Printf("[ERROR] %s", err)
		return err
	}
	return nil
}

// EmitIssueRequest is the interface used to communicate via RPC.
type EmitIssueRequest struct {
	Rule     *RuleObject
	Message  string
	Location hcl.Range
	Meta     Metadata
}

// EmitIssue emits attributes to build the issue to the host process
// Note that the passed rule need to be converted to generic objects
// because the custom structure defined in the plugin cannot be sent via RPC.
func (c *Client) EmitIssue(rule Rule, message string, location hcl.Range, meta Metadata) error {
	req := &EmitIssueRequest{
		Rule:     newObjectFromRule(rule),
		Message:  message,
		Location: location,
		Meta:     meta,
	}
	if err := c.rpcClient.Call("Plugin.EmitIssue", &req, new(interface{})); err != nil {
		return err
	}
	return nil
}

// EnsureNoError is a helper for processing when no error occurs
// This function skips processing without returning an error to the caller when the error is warning
func (*Client) EnsureNoError(err error, proc func() error) error {
	if err == nil {
		return proc()
	}

	if appErr, ok := err.(*Error); ok {
		switch appErr.Level {
		case WarningLevel:
			return nil
		case ErrorLevel:
			return appErr
		default:
			panic(appErr)
		}
	} else {
		return err
	}
}