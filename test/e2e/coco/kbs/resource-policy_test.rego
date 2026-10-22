package policy_test

import rego.v1

import data.policy

bound := "q83vAA=="

claims(init_data) := {"submods": {"cpu0": {"ear.veraison.annotated-evidence": {
	"sample": {"svn": "1"},
	"init_data": init_data,
}}}}

test_the_bound_init_data_gets_the_key if {
	policy.allow with input as claims(bound)
		with data.plugin as "resource"
		with data["resource-path"] as ["default", "hangar-e2e", "tls-key"]
		with policy.bound_init_data as bound
}

test_other_init_data_gets_no_key if {
	not policy.allow with input as claims("AAAAAA==")
		with data.plugin as "resource"
		with data["resource-path"] as ["default", "hangar-e2e", "tls-key"]
		with policy.bound_init_data as bound
}

test_no_init_data_gets_no_key if {
	not policy.allow with input as {"submods": {"cpu0": {"ear.veraison.annotated-evidence": {"sample": {"svn": "1"}}}}}
		with data.plugin as "resource"
		with data["resource-path"] as ["default", "hangar-e2e", "tls-key"]
		with policy.bound_init_data as bound
}

test_the_bound_init_data_gets_no_other_resource if {
	not policy.allow with input as claims(bound)
		with data.plugin as "resource"
		with data["resource-path"] as ["default", "hangar-e2e", "other-key"]
		with policy.bound_init_data as bound
}

test_the_unrendered_placeholder_admits_nothing if {
	not policy.allow with input as claims(bound)
		with data.plugin as "resource"
		with data["resource-path"] as ["default", "hangar-e2e", "tls-key"]
}
