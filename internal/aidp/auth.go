package aidp

import "github.com/oracle/oci-go-sdk/v65/common"

// APIKeySigner uses the OCI configuration file and API key for the chosen profile.
// Only API-key signing has been validated; other OCI authentication modes are
// not selected automatically.
func APIKeySigner(configPath, profile string) Signer {
	return common.DefaultRequestSigner(common.CustomProfileConfigProvider(configPath, profile))
}
