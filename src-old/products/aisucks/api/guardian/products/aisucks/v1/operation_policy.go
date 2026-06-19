package aisucksv1

import (
	policyv1 "github.com/guardian-intelligence/guardian/src/products/aisucks/api/guardian/policy/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func OperationPolicy(procedure string) (*policyv1.OperationPolicy, bool) {
	switch procedure {
	case AisucksServiceHealthProcedure:
		return methodPolicy("AisucksService", "Health")
	default:
		return nil, false
	}
}

func methodPolicy(serviceName, methodName protoreflect.Name) (*policyv1.OperationPolicy, bool) {
	service := File_guardian_products_aisucks_v1_aisucks_proto.Services().ByName(serviceName)
	if service == nil {
		return nil, false
	}
	method := service.Methods().ByName(methodName)
	if method == nil || method.Options() == nil {
		return nil, false
	}
	ext := proto.GetExtension(method.Options(), policyv1.E_Operation)
	policy, ok := ext.(*policyv1.OperationPolicy)
	if !ok || policy == nil {
		return nil, false
	}
	clone, ok := proto.Clone(policy).(*policyv1.OperationPolicy)
	return clone, ok
}
