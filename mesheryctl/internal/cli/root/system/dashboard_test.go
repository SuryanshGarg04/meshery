// Copyright Meshery Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package system

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShouldUseEphemeralPortFallback(t *testing.T) {
	tests := []struct {
		name              string
		portExplicitlySet bool
		requestedPort     int
		expected          bool
	}{
		{
			name:              "given default port when port not explicitly set then fallback is enabled",
			portExplicitlySet: false,
			requestedPort:     defaultPort,
			expected:          true,
		},
		{
			name:              "given custom port when port explicitly set then fallback is disabled",
			portExplicitlySet: true,
			requestedPort:     8080,
			expected:          false,
		},
		{
			name:              "given default port when port explicitly set then fallback is disabled",
			portExplicitlySet: true,
			requestedPort:     defaultPort,
			expected:          false,
		},
		{
			name:              "given non default port when port not explicitly set then fallback is disabled",
			portExplicitlySet: false,
			requestedPort:     8080,
			expected:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := shouldUseEphemeralPortFallback(tt.portExplicitlySet, tt.requestedPort)
			assert.Equal(t, tt.expected, actual)
		})
	}
}
