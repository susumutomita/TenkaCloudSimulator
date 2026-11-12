package aws

import (
	"strings"
	"testing"
)

func TestCompileStackRefusesMalformedTemplates(t *testing.T) {
	param := func(value string) string {
		return "Resources:\n  P:\n    Type: AWS::SSM::Parameter\n    Properties:\n      Name: /compile/p\n      Type: String\n      Value: " + value + "\n"
	}
	deep := "Resources:\n  P:\n    Type: AWS::SSM::Parameter\n    Properties:\n      Name: /compile/p\n      Type: String\n      Value: " + strings.Repeat("[", 80) + "x" + strings.Repeat("]", 80) + "\n"
	cases := map[string]string{
		"over-deep value":          deep,
		"unknown Ref":              param("!Ref Missing"),
		"Ref with extra key":       param("{Ref: AWS::Region, Extra: 1}"),
		"unknown GetAtt":           param("!GetAtt Missing.Arn"),
		"GetAtt with extra key":    param("{'Fn::GetAtt': [P, Value], Extra: 1}"),
		"non-scalar Sub":           param("{'Fn::Sub': [x, {}]}"),
		"Sub with extra key":       param("{'Fn::Sub': x, Extra: 1}"),
		"Base64 with extra key":    param("{'Fn::Base64': x, Extra: 1}"),
		"Base64 of a list":         param("{'Fn::Base64': [a]}"),
		"GetAZs of a list":         param("{'Fn::Select': [0, {'Fn::GetAZs': [a]}]}"),
		"Select null item":         param("{'Fn::Select': [0, [~, a]]}"),
		"Select out of range":      param("!Select [3, [a, b]]"),
		"Select negative index":    param("!Select ['-1', [a, b]]"),
		"Select non-list":          param("!Select [0, a]"),
		"shorthand Select scalar":  param("!Select a"),
		"unsupported intrinsic":    param("{'Fn::Join': ['', [a, b]]}"),
		"unknown short tag":        param("!Join ['', [a]]"),
		"parameters not object":    "Parameters: [a]\n" + param("a"),
		"conditions not object":    "Conditions: [a]\n" + param("a"),
		"invalid logical ID":       "Resources:\n  bad-id:\n    Type: AWS::SSM::Parameter\n    Properties: {Name: /compile/p, Type: String, Value: a}\n",
		"logical ID = parameter":   "Parameters:\n  P: {Type: String, Default: a}\n" + param("a"),
		"outputs not object":       param("a") + "Outputs: [a]\n",
		"output not object":        param("a") + "Outputs:\n  O: a\n",
		"output without Value":     param("a") + "Outputs:\n  O: {Description: d}\n",
		"output bad description":   param("a") + "Outputs:\n  O: {Value: a, Description: [d]}\n",
		"output bad attribute":     param("a") + "Outputs:\n  O: {Value: a, Export: {Name: x}}\n",
		"output unknown condition": param("a") + "Outputs:\n  O: {Value: a, Condition: Missing}\n",
		"output unknown Ref":       param("a") + "Outputs:\n  O: {Value: !Ref Missing}\n",
		"output non-string":        param("a") + "Outputs:\n  O: {Value: [a, b]}\n",
		"unsupported section":      "Transform: AWS::Serverless-2016-10-31\n" + param("a"),
		"no resources":             "Resources: {}\n",
		"unsupported resource":     "Resources:\n  Q:\n    Type: AWS::SQS::Queue\n",
		"unsupported attribute":    "Resources:\n  P:\n    Type: AWS::SSM::Parameter\n    DeletionPolicy: Retain\n    Properties: {Name: /compile/p, Type: String, Value: a}\n",
	}
	for name, body := range cases {
		if _, err := compileStack("us-east-1", "compile", body, nil, ""); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// The same shapes compile when well formed.
	for name, body := range map[string]string{
		"Select":   param("!Select [1, [a, b]]"),
		"GetAZs":   param("{'Fn::Select': [0, {'Fn::GetAZs': ''}]}"),
		"Base64":   param("{'Fn::Base64': plain}"),
		"Sub":      param("!Sub '${AWS::Region}-x'"),
		"outputs":  param("a") + "Outputs:\n  O: {Value: !Ref P, Description: d}\n",
		"sections": "AWSTemplateFormatVersion: '2010-09-09'\nDescription: d\n" + param("a"),
	} {
		if _, err := compileStack("us-east-1", "compile", body, nil, ""); err != nil {
			t.Errorf("well-formed %s refused: %v", name, err)
		}
	}
}
