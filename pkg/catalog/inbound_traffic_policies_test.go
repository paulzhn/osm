package catalog

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	mapset "github.com/deckarep/golang-set"
	"github.com/golang/mock/gomock"
	access "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/access/v1alpha3"
	spec "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/specs/v1alpha4"
	split "github.com/servicemeshinterface/smi-sdk-go/pkg/apis/split/v1alpha2"
	tassert "github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openservicemesh/osm/pkg/apis/config/v1alpha2"
	policyv1alpha1 "github.com/openservicemesh/osm/pkg/apis/policy/v1alpha1"
	tresorFake "github.com/openservicemesh/osm/pkg/certificate/providers/tresor/fake"
	"github.com/openservicemesh/osm/pkg/compute/kube"
	"github.com/openservicemesh/osm/pkg/constants"
	configFake "github.com/openservicemesh/osm/pkg/gen/client/config/clientset/versioned/fake"
	"github.com/openservicemesh/osm/pkg/identity"
	"github.com/openservicemesh/osm/pkg/k8s"
	"github.com/openservicemesh/osm/pkg/service"
	"github.com/openservicemesh/osm/pkg/tests"
	"github.com/openservicemesh/osm/pkg/trafficpolicy"
)

func TestGetInboundMeshTrafficPolicy(t *testing.T) {
	upstreamSvcAccount := identity.K8sServiceAccount{Namespace: "ns1", Name: "sa1"}
	perRouteLocalRateLimitConfig := &policyv1alpha1.HTTPPerRouteRateLimitSpec{
		Local: &policyv1alpha1.HTTPLocalRateLimitSpec{
			Requests: 10,
			Unit:     "second",
		},
	}
	virtualHostLocalRateLimitConfig := &policyv1alpha1.RateLimitSpec{
		Local: &policyv1alpha1.LocalRateLimitSpec{
			HTTP: &policyv1alpha1.HTTPLocalRateLimitSpec{
				Requests: 100,
				Unit:     "minute",
			},
		},
	}
	perRouteGlobalRateLimitConfig := &policyv1alpha1.HTTPPerRouteRateLimitSpec{
		Global: &policyv1alpha1.HTTPGlobalPerRouteRateLimitSpec{},
	}
	virtualHostGlobalRateLimitConfig := &policyv1alpha1.RateLimitSpec{
		Global: &policyv1alpha1.GlobalRateLimitSpec{
			TCP: &policyv1alpha1.TCPGlobalRateLimitSpec{
				RateLimitService: policyv1alpha1.RateLimitServiceSpec{
					Host: "foo.bar",
					Port: 8080,
				},
			},
			HTTP: &policyv1alpha1.HTTPGlobalRateLimitSpec{
				RateLimitService: policyv1alpha1.RateLimitServiceSpec{
					Host: "foo.bar",
					Port: 8080,
				},
			},
		},
	}

	testCases := []struct {
		name                    string
		upstreamIdentity        identity.ServiceIdentity
		upstreamServices        []service.MeshService
		permissiveMode          bool
		trafficTargets          []*access.TrafficTarget
		httpRouteGroups         []*spec.HTTPRouteGroup
		tcpRoutes               []*spec.TCPRoute
		trafficSplits           []*split.TrafficSplit
		upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting
		newTrustDomain          string
		spiffeEnabled           bool
		prepare                 func(mockK8s *k8s.MockController,
			trafficSplits []*split.TrafficSplit,
			trafficTargets []*access.TrafficTarget,
			upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting,
		)
		expectedInboundMeshHTTPRouteConfigsPerPort map[int][]*trafficpolicy.InboundTrafficPolicy
		expectedInboundMeshClusterConfigs          []*trafficpolicy.MeshClusterConfig
		expectedInboundMeshTrafficMatches          []*trafficpolicy.TrafficMatch
	}{
		{
			name:             "multiple services, SMI mode, 1 TrafficTarget, 1 HTTPRouteGroup, 0 TrafficSplit",
			upstreamIdentity: upstreamSvcAccount.ToServiceIdentity(),
			upstreamServices: []service.MeshService{
				{
					Name:       "s1",
					Namespace:  "ns1",
					Port:       80,
					TargetPort: 8080,
					Protocol:   "http",
				},
				{
					Name:       "s2",
					Namespace:  "ns1",
					Port:       90,
					TargetPort: 9090,
					Protocol:   "http",
				},
			},
			permissiveMode: false,
			trafficTargets: []*access.TrafficTarget{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "access.smi-spec.io/v1alpha3",
						Kind:       "TrafficTarget",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "t1",
						Namespace: "ns1",
					},
					Spec: access.TrafficTargetSpec{
						Destination: access.IdentityBindingSubject{
							Kind:      "ServiceAccount",
							Name:      "sa1",
							Namespace: "ns1",
						},
						Sources: []access.IdentityBindingSubject{{
							Kind:      "ServiceAccount",
							Name:      "sa2",
							Namespace: "ns2",
						}},
						Rules: []access.TrafficTargetRule{{
							Kind:    "HTTPRouteGroup",
							Name:    "rule-1",
							Matches: []string{"route-1"},
						}},
					},
				},
			},
			httpRouteGroups: []*spec.HTTPRouteGroup{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "specs.smi-spec.io/v1alpha4",
						Kind:       "HTTPRouteGroup",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "rule-1",
					},
					Spec: spec.HTTPRouteGroupSpec{
						Matches: []spec.HTTPMatch{
							{
								Name:      "route-1",
								PathRegex: "/get",
								Methods:   []string{"GET"},
								Headers: map[string]string{
									"foo": "bar",
								},
							},
						},
					},
				},
			},
			trafficSplits: nil,
			prepare: func(mockK8s *k8s.MockController, trafficSplits []*split.TrafficSplit, trafficTargets []*access.TrafficTarget, upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting) {
				mockK8s.EXPECT().ListUpstreamTrafficSettings().Return(upstreamTrafficSettings).AnyTimes()
				mockK8s.EXPECT().ListTrafficSplits().Return(trafficSplits).AnyTimes()
				mockK8s.EXPECT().ListTrafficTargets().Return(trafficTargets).AnyTimes()
				mockK8s.EXPECT().ListMeshRootCertificates().Return(nil, nil).AnyTimes()
			},
			expectedInboundMeshHTTPRouteConfigsPerPort: map[int][]*trafficpolicy.InboundTrafficPolicy{
				8080: {
					{
						Name: "s1.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1",
							"s1:80",
							"s1.ns1",
							"s1.ns1:80",
							"s1.ns1.svc",
							"s1.ns1.svc:80",
							"s1.ns1.svc.cluster",
							"s1.ns1.svc.cluster:80",
							"s1.ns1.svc.cluster.local",
							"s1.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1|8080|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false)),
							},
						},
					},
				},
				9090: {
					{
						Name: "s2.ns1.svc.cluster.local",
						Hostnames: []string{
							"s2",
							"s2:90",
							"s2.ns1",
							"s2.ns1:90",
							"s2.ns1.svc",
							"s2.ns1.svc:90",
							"s2.ns1.svc.cluster",
							"s2.ns1.svc.cluster:90",
							"s2.ns1.svc.cluster.local",
							"s2.ns1.svc.cluster.local:90",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s2|9090|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false)),
							},
						},
					},
				},
			},
			expectedInboundMeshClusterConfigs: []*trafficpolicy.MeshClusterConfig{
				{
					Name:    "ns1/s1|8080|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1", Port: 80, TargetPort: 8080, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    8080,
				},
				{
					Name:    "ns1/s2|9090|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s2", Port: 90, TargetPort: 9090, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    9090,
				},
			},
		},
		{
			name:             "multiple services, statefulset, SMI mode, 1 TrafficTarget, 1 TCPRoute, 0 TrafficSplit",
			upstreamIdentity: upstreamSvcAccount.ToServiceIdentity(),
			upstreamServices: []service.MeshService{
				{
					Name:       "mysql-0.mysql",
					Namespace:  "ns1",
					Port:       3306,
					TargetPort: 3306,
					Protocol:   "tcp",
				},
				{
					Name:       "s2",
					Namespace:  "ns1",
					Port:       90,
					TargetPort: 9090,
					Protocol:   "http",
				},
			},
			permissiveMode: false,
			trafficTargets: []*access.TrafficTarget{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "access.smi-spec.io/v1alpha3",
						Kind:       "TrafficTarget",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "t1",
						Namespace: "ns1",
					},
					Spec: access.TrafficTargetSpec{
						Destination: access.IdentityBindingSubject{
							Kind:      "ServiceAccount",
							Name:      "sa1",
							Namespace: "ns1",
						},
						Sources: []access.IdentityBindingSubject{{
							Kind:      "ServiceAccount",
							Name:      "sa2",
							Namespace: "ns2",
						}},
						Rules: []access.TrafficTargetRule{{
							Kind: "TCPRoute",
							Name: "rule-1",
						}},
					},
				},
			},
			tcpRoutes: []*spec.TCPRoute{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "specs.smi-spec.io/v1alpha4",
						Kind:       "TCPRoute",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "rule-1",
					},
					Spec: spec.TCPRouteSpec{
						Matches: spec.TCPMatch{
							Ports: []int{3306},
						},
					},
				},
			},
			trafficSplits: nil,
			prepare: func(mockK8s *k8s.MockController, trafficSplits []*split.TrafficSplit, trafficTargets []*access.TrafficTarget, upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting) {
				mockK8s.EXPECT().ListTrafficSplits().Return(trafficSplits).AnyTimes()
				mockK8s.EXPECT().ListMeshRootCertificates().Return(nil, nil).AnyTimes()
			},
			expectedInboundMeshTrafficMatches: []*trafficpolicy.TrafficMatch{
				{
					Name:                "inbound_ns1/mysql-0.mysql_3306_tcp",
					DestinationPort:     3306,
					DestinationProtocol: "tcp",
					ServerNames:         []string{"mysql-0.mysql.ns1.svc.cluster.local"},
					Cluster:             "ns1/mysql-0.mysql|3306|local",
				},
				{
					Name:                "inbound_ns1/s2_9090_http",
					DestinationPort:     9090,
					DestinationProtocol: "http",
					ServerNames:         []string{"s2.ns1.svc.cluster.local"},
					Cluster:             "ns1/s2|9090|local",
				},
			},
			expectedInboundMeshClusterConfigs: []*trafficpolicy.MeshClusterConfig{
				{
					Name:    "ns1/mysql-0.mysql|3306|local",
					Service: service.MeshService{Namespace: "ns1", Name: "mysql-0.mysql", Port: 3306, TargetPort: 3306, Protocol: "tcp"},
					Address: "127.0.0.1",
					Port:    3306,
				},
				{
					Name:    "ns1/s2|9090|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s2", Port: 90, TargetPort: 9090, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    9090,
				},
			},
		},
		{
			name:             "multiple services, SMI mode, 1 TrafficTarget, multiple HTTPRouteGroup, 0 TrafficSplit",
			upstreamIdentity: upstreamSvcAccount.ToServiceIdentity(),
			upstreamServices: []service.MeshService{
				{
					Name:       "s1",
					Namespace:  "ns1",
					Port:       80,
					TargetPort: 80,
					Protocol:   "http",
				},
				{
					Name:       "s2",
					Namespace:  "ns1",
					Port:       90,
					TargetPort: 90,
					Protocol:   "http",
				},
			},
			permissiveMode: false,
			trafficTargets: []*access.TrafficTarget{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "access.smi-spec.io/v1alpha3",
						Kind:       "TrafficTarget",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "t1",
						Namespace: "ns1",
					},
					Spec: access.TrafficTargetSpec{
						Destination: access.IdentityBindingSubject{
							Kind:      "ServiceAccount",
							Name:      "sa1",
							Namespace: "ns1",
						},
						Sources: []access.IdentityBindingSubject{{
							Kind:      "ServiceAccount",
							Name:      "sa2",
							Namespace: "ns2",
						}},
						Rules: []access.TrafficTargetRule{
							{
								Kind:    "HTTPRouteGroup",
								Name:    "rule-1",
								Matches: []string{"route-1"},
							},
							{
								Kind:    "HTTPRouteGroup",
								Name:    "rule-2",
								Matches: []string{"route-2"},
							},
						},
					},
				},
			},
			httpRouteGroups: []*spec.HTTPRouteGroup{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "specs.smi-spec.io/v1alpha4",
						Kind:       "HTTPRouteGroup",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "rule-1",
					},
					Spec: spec.HTTPRouteGroupSpec{
						Matches: []spec.HTTPMatch{
							{
								Name:      "route-1",
								PathRegex: "/get",
								Methods:   []string{"GET"},
								Headers: map[string]string{
									"foo": "bar",
								},
							},
						},
					},
				},
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "specs.smi-spec.io/v1alpha4",
						Kind:       "HTTPRouteGroup",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "rule-2",
					},
					Spec: spec.HTTPRouteGroupSpec{
						Matches: []spec.HTTPMatch{
							{
								Name:      "route-2",
								PathRegex: "/put",
								Methods:   []string{"PUT"},
								Headers: map[string]string{
									"foo": "bar",
								},
							},
						},
					},
				},
			},
			trafficSplits: nil,
			prepare: func(mockK8s *k8s.MockController, trafficSplits []*split.TrafficSplit, trafficTargets []*access.TrafficTarget, upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting) {
				mockK8s.EXPECT().ListTrafficSplits().Return(trafficSplits).AnyTimes()
				mockK8s.EXPECT().ListMeshRootCertificates().Return(nil, nil).AnyTimes()
			},
			expectedInboundMeshHTTPRouteConfigsPerPort: map[int][]*trafficpolicy.InboundTrafficPolicy{
				80: {
					{
						Name: "s1.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1",
							"s1:80",
							"s1.ns1",
							"s1.ns1:80",
							"s1.ns1.svc",
							"s1.ns1.svc:80",
							"s1.ns1.svc.cluster",
							"s1.ns1.svc.cluster:80",
							"s1.ns1.svc.cluster.local",
							"s1.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false)),
							},
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/put",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"PUT"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false)),
							},
						},
					},
				},
				90: {
					{
						Name: "s2.ns1.svc.cluster.local",
						Hostnames: []string{
							"s2",
							"s2:90",
							"s2.ns1",
							"s2.ns1:90",
							"s2.ns1.svc",
							"s2.ns1.svc:90",
							"s2.ns1.svc.cluster",
							"s2.ns1.svc.cluster:90",
							"s2.ns1.svc.cluster.local",
							"s2.ns1.svc.cluster.local:90",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s2|90|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false)),
							},
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/put",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"PUT"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s2|90|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false)),
							},
						},
					},
				},
			},
			expectedInboundMeshClusterConfigs: []*trafficpolicy.MeshClusterConfig{
				{
					Name:    "ns1/s1|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s2|90|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s2", Port: 90, TargetPort: 90, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    90,
				},
			},
		},
		{
			name:             "multiple services, SMI mode, 1 TrafficTarget, 1 HTTPRouteGroup, 1 TrafficSplit",
			upstreamIdentity: upstreamSvcAccount.ToServiceIdentity(),
			upstreamServices: []service.MeshService{
				{
					Name:       "s1",
					Namespace:  "ns1",
					Port:       80,
					TargetPort: 80,
					Protocol:   "http",
				},
				{
					Name:       "s2",
					Namespace:  "ns1",
					Port:       90,
					TargetPort: 90,
					Protocol:   "http",
				},
			},
			permissiveMode: false,
			trafficTargets: []*access.TrafficTarget{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "access.smi-spec.io/v1alpha3",
						Kind:       "TrafficTarget",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "t1",
						Namespace: "ns1",
					},
					Spec: access.TrafficTargetSpec{
						Destination: access.IdentityBindingSubject{
							Kind:      "ServiceAccount",
							Name:      "sa1",
							Namespace: "ns1",
						},
						Sources: []access.IdentityBindingSubject{{
							Kind:      "ServiceAccount",
							Name:      "sa2",
							Namespace: "ns2",
						}},
						Rules: []access.TrafficTargetRule{{
							Kind:    "HTTPRouteGroup",
							Name:    "rule-1",
							Matches: []string{"route-1"},
						}},
					},
				},
			},
			httpRouteGroups: []*spec.HTTPRouteGroup{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "specs.smi-spec.io/v1alpha4",
						Kind:       "HTTPRouteGroup",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "rule-1",
					},
					Spec: spec.HTTPRouteGroupSpec{
						Matches: []spec.HTTPMatch{
							{
								Name:      "route-1",
								PathRegex: "/get",
								Methods:   []string{"GET"},
								Headers: map[string]string{
									"foo": "bar",
								},
							},
						},
					},
				},
			},
			trafficSplits: []*split.TrafficSplit{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "split1",
					},
					Spec: split.TrafficSplitSpec{
						Service: "s1-apex",
						Backends: []split.TrafficSplitBackend{
							{
								Service: "s1",
								Weight:  10,
							},
							{
								Service: "s-unused",
								Weight:  90,
							},
						},
					},
				},
			},
			prepare: func(mockK8s *k8s.MockController, trafficSplits []*split.TrafficSplit, trafficTargets []*access.TrafficTarget, upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting) {
				mockK8s.EXPECT().ListTrafficSplits().Return(trafficSplits).AnyTimes()
				mockK8s.EXPECT().ListMeshRootCertificates().Return(nil, nil).AnyTimes()
			},
			expectedInboundMeshHTTPRouteConfigsPerPort: map[int][]*trafficpolicy.InboundTrafficPolicy{
				80: {
					{
						Name: "s1.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1",
							"s1:80",
							"s1.ns1",
							"s1.ns1:80",
							"s1.ns1.svc",
							"s1.ns1.svc:80",
							"s1.ns1.svc.cluster",
							"s1.ns1.svc.cluster:80",
							"s1.ns1.svc.cluster.local",
							"s1.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false)),
							},
						},
					},
					{
						Name: "s1-apex.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1-apex",
							"s1-apex:80",
							"s1-apex.ns1",
							"s1-apex.ns1:80",
							"s1-apex.ns1.svc",
							"s1-apex.ns1.svc:80",
							"s1-apex.ns1.svc.cluster",
							"s1-apex.ns1.svc.cluster:80",
							"s1-apex.ns1.svc.cluster.local",
							"s1-apex.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1-apex|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false)),
							},
						},
					},
				},
				90: {
					{
						Name: "s2.ns1.svc.cluster.local",
						Hostnames: []string{
							"s2",
							"s2:90",
							"s2.ns1",
							"s2.ns1:90",
							"s2.ns1.svc",
							"s2.ns1.svc:90",
							"s2.ns1.svc.cluster",
							"s2.ns1.svc.cluster:90",
							"s2.ns1.svc.cluster.local",
							"s2.ns1.svc.cluster.local:90",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s2|90|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false)),
							},
						},
					},
				},
			},
			expectedInboundMeshClusterConfigs: []*trafficpolicy.MeshClusterConfig{
				{
					Name:    "ns1/s1|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s1-apex|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1-apex", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s2|90|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s2", Port: 90, TargetPort: 90, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    90,
				},
			},
		},
		{
			name:             "multiple services, permissive mode, 1 TrafficSplit",
			upstreamIdentity: upstreamSvcAccount.ToServiceIdentity(),
			upstreamServices: []service.MeshService{
				{
					Name:       "s1",
					Namespace:  "ns1",
					Port:       80,
					TargetPort: 80,
					Protocol:   "http",
				},
				{
					Name:       "s2",
					Namespace:  "ns1",
					Port:       90,
					TargetPort: 90,
					Protocol:   "http",
				},
			},
			permissiveMode:  true,
			trafficTargets:  nil,
			httpRouteGroups: nil,
			trafficSplits: []*split.TrafficSplit{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "split1",
					},
					Spec: split.TrafficSplitSpec{
						Service: "s1-apex",
						Backends: []split.TrafficSplitBackend{
							{
								Service: "s1",
								Weight:  10,
							},
							{
								Service: "s-unused",
								Weight:  90,
							},
						},
					},
				},
			},
			prepare: func(mockK8s *k8s.MockController, trafficSplits []*split.TrafficSplit, trafficTargets []*access.TrafficTarget, upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting) {
				mockK8s.EXPECT().ListTrafficSplits().Return(trafficSplits).AnyTimes()
				mockK8s.EXPECT().ListMeshRootCertificates().Return(nil, nil).AnyTimes()
			},
			expectedInboundMeshHTTPRouteConfigsPerPort: map[int][]*trafficpolicy.InboundTrafficPolicy{
				80: {
					{
						Name: "s1.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1",
							"s1:80",
							"s1.ns1",
							"s1.ns1:80",
							"s1.ns1.svc",
							"s1.ns1.svc:80",
							"s1.ns1.svc.cluster",
							"s1.ns1.svc.cluster:80",
							"s1.ns1.svc.cluster.local",
							"s1.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.WildCardRouteMatch,
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.WildcardPrincipal),
							},
						},
					},
					{
						Name: "s1-apex.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1-apex",
							"s1-apex:80",
							"s1-apex.ns1",
							"s1-apex.ns1:80",
							"s1-apex.ns1.svc",
							"s1-apex.ns1.svc:80",
							"s1-apex.ns1.svc.cluster",
							"s1-apex.ns1.svc.cluster:80",
							"s1-apex.ns1.svc.cluster.local",
							"s1-apex.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.WildCardRouteMatch,
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1-apex|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.WildcardPrincipal),
							},
						},
					},
				},
				90: {
					{
						Name: "s2.ns1.svc.cluster.local",
						Hostnames: []string{
							"s2",
							"s2:90",
							"s2.ns1",
							"s2.ns1:90",
							"s2.ns1.svc",
							"s2.ns1.svc:90",
							"s2.ns1.svc.cluster",
							"s2.ns1.svc.cluster:90",
							"s2.ns1.svc.cluster.local",
							"s2.ns1.svc.cluster.local:90",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.WildCardRouteMatch,
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s2|90|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.WildcardPrincipal),
							},
						},
					},
				},
			},
			expectedInboundMeshClusterConfigs: []*trafficpolicy.MeshClusterConfig{
				{
					Name:    "ns1/s1|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s1-apex|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1-apex", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s2|90|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s2", Port: 90, TargetPort: 90, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    90,
				},
			},
		},
		{
			name: "multiple services with different protocol, SMI mode, 1 TrafficTarget, 1 HTTPRouteGroup, 0 TrafficSplit",
			// Ports ns1/s2:90 and ns1/s3:91 use TCP, so HTTP route configs for them should not be built
			upstreamIdentity: upstreamSvcAccount.ToServiceIdentity(),
			upstreamServices: []service.MeshService{
				{
					Name:       "s1",
					Namespace:  "ns1",
					Port:       80,
					TargetPort: 80,
					Protocol:   "http",
				},
				{
					Name:       "s2",
					Namespace:  "ns1",
					Port:       90,
					TargetPort: 90,
					Protocol:   "tcp",
				},
				{
					Name:       "s3",
					Namespace:  "ns1",
					Port:       91,
					TargetPort: 91,
					Protocol:   "tcp-server-first",
				},
			},
			permissiveMode: false,
			trafficTargets: []*access.TrafficTarget{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "access.smi-spec.io/v1alpha3",
						Kind:       "TrafficTarget",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "t1",
						Namespace: "ns1",
					},
					Spec: access.TrafficTargetSpec{
						Destination: access.IdentityBindingSubject{
							Kind:      "ServiceAccount",
							Name:      "sa1",
							Namespace: "ns1",
						},
						Sources: []access.IdentityBindingSubject{{
							Kind:      "ServiceAccount",
							Name:      "sa2",
							Namespace: "ns2",
						}},
						Rules: []access.TrafficTargetRule{{
							Kind:    "HTTPRouteGroup",
							Name:    "rule-1",
							Matches: []string{"route-1"},
						}},
					},
				},
			},
			httpRouteGroups: []*spec.HTTPRouteGroup{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "specs.smi-spec.io/v1alpha4",
						Kind:       "HTTPRouteGroup",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "rule-1",
					},
					Spec: spec.HTTPRouteGroupSpec{
						Matches: []spec.HTTPMatch{
							{
								Name:      "route-1",
								PathRegex: "/get",
								Methods:   []string{"GET"},
								Headers: map[string]string{
									"foo": "bar",
								},
							},
						},
					},
				},
			},
			trafficSplits: nil,
			prepare: func(mockK8s *k8s.MockController, trafficSplits []*split.TrafficSplit, trafficTargets []*access.TrafficTarget, upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting) {
				mockK8s.EXPECT().ListTrafficSplits().Return(trafficSplits).AnyTimes()
				mockK8s.EXPECT().ListMeshRootCertificates().Return(nil, nil).AnyTimes()
			},
			expectedInboundMeshHTTPRouteConfigsPerPort: map[int][]*trafficpolicy.InboundTrafficPolicy{
				80: {
					{
						Name: "s1.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1",
							"s1:80",
							"s1.ns1",
							"s1.ns1:80",
							"s1.ns1.svc",
							"s1.ns1.svc:80",
							"s1.ns1.svc.cluster",
							"s1.ns1.svc.cluster:80",
							"s1.ns1.svc.cluster.local",
							"s1.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet("sa2.ns2.cluster.local"),
							},
						},
					},
				},
			},
			expectedInboundMeshClusterConfigs: []*trafficpolicy.MeshClusterConfig{
				{
					Name:    "ns1/s1|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s2|90|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s2", Port: 90, TargetPort: 90, Protocol: "tcp"},
					Address: "127.0.0.1",
					Port:    90,
				},
				{
					Name:    "ns1/s3|91|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s3", Port: 91, TargetPort: 91, Protocol: "tcp-server-first"},
					Address: "127.0.0.1",
					Port:    91,
				},
			},
		},
		{
			name: "multiple services, SMI mode, multiple TrafficTarget with same routes but different allowed clients",
			// This test configures multiple TrafficTarget resources with the same route that different downstream clients are
			// allowed to access. The test verifies that routing rules with the same route are correctly merged to a single routing
			// rule with merged downstream client identities.
			upstreamIdentity: upstreamSvcAccount.ToServiceIdentity(),
			upstreamServices: []service.MeshService{
				{
					Name:       "s1",
					Namespace:  "ns1",
					Port:       80,
					TargetPort: 80,
					Protocol:   "http",
				},
				{
					Name:       "s2",
					Namespace:  "ns1",
					Port:       90,
					TargetPort: 90,
					Protocol:   "http",
				},
			},
			permissiveMode: false,
			trafficTargets: []*access.TrafficTarget{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "access.smi-spec.io/v1alpha3",
						Kind:       "TrafficTarget",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "t1",
						Namespace: "ns1",
					},
					Spec: access.TrafficTargetSpec{
						Destination: access.IdentityBindingSubject{
							Kind:      "ServiceAccount",
							Name:      "sa1",
							Namespace: "ns1",
						},
						Sources: []access.IdentityBindingSubject{{
							Kind:      "ServiceAccount",
							Name:      "sa2",
							Namespace: "ns2",
						}},
						Rules: []access.TrafficTargetRule{{
							Kind:    "HTTPRouteGroup",
							Name:    "rule-1",
							Matches: []string{"route-1"},
						}},
					},
				},
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "access.smi-spec.io/v1alpha3",
						Kind:       "TrafficTarget",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "t1",
						Namespace: "ns1",
					},
					Spec: access.TrafficTargetSpec{
						Destination: access.IdentityBindingSubject{
							Kind:      "ServiceAccount",
							Name:      "sa1",
							Namespace: "ns1",
						},
						Sources: []access.IdentityBindingSubject{{
							Kind:      "ServiceAccount",
							Name:      "sa3",
							Namespace: "ns3",
						}},
						Rules: []access.TrafficTargetRule{{
							Kind:    "HTTPRouteGroup",
							Name:    "rule-1",
							Matches: []string{"route-1"},
						}},
					},
				},
			},
			httpRouteGroups: []*spec.HTTPRouteGroup{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "specs.smi-spec.io/v1alpha4",
						Kind:       "HTTPRouteGroup",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "rule-1",
					},
					Spec: spec.HTTPRouteGroupSpec{
						Matches: []spec.HTTPMatch{
							{
								Name:      "route-1",
								PathRegex: "/get",
								Methods:   []string{"GET"},
								Headers: map[string]string{
									"foo": "bar",
								},
							},
						},
					},
				},
			},
			trafficSplits: nil,
			prepare: func(mockK8s *k8s.MockController, trafficSplits []*split.TrafficSplit, trafficTargets []*access.TrafficTarget, upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting) {
				mockK8s.EXPECT().ListTrafficSplits().Return(trafficSplits).AnyTimes()
				mockK8s.EXPECT().ListMeshRootCertificates().Return(nil, nil).AnyTimes()
			},
			expectedInboundMeshHTTPRouteConfigsPerPort: map[int][]*trafficpolicy.InboundTrafficPolicy{
				80: {
					{
						Name: "s1.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1",
							"s1:80",
							"s1.ns1",
							"s1.ns1:80",
							"s1.ns1.svc",
							"s1.ns1.svc:80",
							"s1.ns1.svc.cluster",
							"s1.ns1.svc.cluster:80",
							"s1.ns1.svc.cluster.local",
							"s1.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(
									identity.K8sServiceAccount{
										Name:      "sa2",
										Namespace: "ns2",
									}.AsPrincipal("cluster.local", false),
									identity.K8sServiceAccount{
										Name:      "sa3",
										Namespace: "ns3",
									}.AsPrincipal("cluster.local", false)),
							},
						},
					},
				},
				90: {
					{
						Name: "s2.ns1.svc.cluster.local",
						Hostnames: []string{
							"s2",
							"s2:90",
							"s2.ns1",
							"s2.ns1:90",
							"s2.ns1.svc",
							"s2.ns1.svc:90",
							"s2.ns1.svc.cluster",
							"s2.ns1.svc.cluster:90",
							"s2.ns1.svc.cluster.local",
							"s2.ns1.svc.cluster.local:90",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s2|90|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(
									identity.K8sServiceAccount{
										Name:      "sa2",
										Namespace: "ns2",
									}.AsPrincipal("cluster.local", false),
									identity.K8sServiceAccount{
										Name:      "sa3",
										Namespace: "ns3",
									}.AsPrincipal("cluster.local", false)),
							},
						},
					},
				},
			},
			expectedInboundMeshClusterConfigs: []*trafficpolicy.MeshClusterConfig{
				{
					Name:    "ns1/s1|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s2|90|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s2", Port: 90, TargetPort: 90, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    90,
				},
			},
		},
		{
			name: "multiple services, SMI mode, 1 TrafficTarget, 1 HTTPRouteGroup, 1 TrafficSplit with backend same as apex",
			// This test configures a TrafficSplit where the backend service is the same as the apex. This is a supported
			// SMI configuration and mimics the e2e test e2e_trafficsplit_recursive_split.go.
			upstreamIdentity: upstreamSvcAccount.ToServiceIdentity(),
			upstreamServices: []service.MeshService{
				{
					Name:       "s1",
					Namespace:  "ns1",
					Port:       80,
					TargetPort: 80,
					Protocol:   "http",
				},
				{
					Name:       "s2",
					Namespace:  "ns1",
					Port:       90,
					TargetPort: 90,
					Protocol:   "http",
				},
			},
			permissiveMode: false,
			trafficTargets: []*access.TrafficTarget{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "access.smi-spec.io/v1alpha3",
						Kind:       "TrafficTarget",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "t1",
						Namespace: "ns1",
					},
					Spec: access.TrafficTargetSpec{
						Destination: access.IdentityBindingSubject{
							Kind:      "ServiceAccount",
							Name:      "sa1",
							Namespace: "ns1",
						},
						Sources: []access.IdentityBindingSubject{{
							Kind:      "ServiceAccount",
							Name:      "sa2",
							Namespace: "ns2",
						}},
						Rules: []access.TrafficTargetRule{{
							Kind:    "HTTPRouteGroup",
							Name:    "rule-1",
							Matches: []string{"route-1"},
						}},
					},
				},
			},
			httpRouteGroups: []*spec.HTTPRouteGroup{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "specs.smi-spec.io/v1alpha4",
						Kind:       "HTTPRouteGroup",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "rule-1",
					},
					Spec: spec.HTTPRouteGroupSpec{
						Matches: []spec.HTTPMatch{
							{
								Name:      "route-1",
								PathRegex: "/get",
								Methods:   []string{"GET"},
								Headers: map[string]string{
									"foo": "bar",
								},
							},
						},
					},
				},
			},
			trafficSplits: []*split.TrafficSplit{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "split1",
					},
					Spec: split.TrafficSplitSpec{
						Service: "s1",
						Backends: []split.TrafficSplitBackend{
							{
								Service: "s1",
								Weight:  100,
							},
						},
					},
				},
			},
			prepare: func(mockK8s *k8s.MockController, trafficSplits []*split.TrafficSplit, trafficTargets []*access.TrafficTarget, upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting) {
				mockK8s.EXPECT().ListTrafficSplits().Return(trafficSplits).AnyTimes()
				mockK8s.EXPECT().ListMeshRootCertificates().Return(nil, nil).AnyTimes()
			},
			expectedInboundMeshHTTPRouteConfigsPerPort: map[int][]*trafficpolicy.InboundTrafficPolicy{
				80: {
					{
						Name: "s1.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1",
							"s1:80",
							"s1.ns1",
							"s1.ns1:80",
							"s1.ns1.svc",
							"s1.ns1.svc:80",
							"s1.ns1.svc.cluster",
							"s1.ns1.svc.cluster:80",
							"s1.ns1.svc.cluster.local",
							"s1.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false)),
							},
						},
					},
				},
				90: {
					{
						Name: "s2.ns1.svc.cluster.local",
						Hostnames: []string{
							"s2",
							"s2:90",
							"s2.ns1",
							"s2.ns1:90",
							"s2.ns1.svc",
							"s2.ns1.svc:90",
							"s2.ns1.svc.cluster",
							"s2.ns1.svc.cluster:90",
							"s2.ns1.svc.cluster.local",
							"s2.ns1.svc.cluster.local:90",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s2|90|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false)),
							},
						},
					},
				},
			},
			expectedInboundMeshClusterConfigs: []*trafficpolicy.MeshClusterConfig{
				{
					Name:    "ns1/s1|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s2|90|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s2", Port: 90, TargetPort: 90, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    90,
				},
			},
		},
		{
			name:             "multiple services, permissive mode, 1 TrafficSplit, MeshService is apex for another MeshService",
			upstreamIdentity: upstreamSvcAccount.ToServiceIdentity(),
			upstreamServices: []service.MeshService{
				{
					Name:       "s1",
					Namespace:  "ns1",
					Port:       80,
					TargetPort: 80,
					Protocol:   "http",
				},
				{
					Name:       "s2-apex", // Also an apex service for ns1/s1
					Namespace:  "ns1",
					Port:       80,
					TargetPort: 80,
					Protocol:   "http",
				},
			},
			permissiveMode:  true,
			trafficTargets:  nil,
			httpRouteGroups: nil,
			trafficSplits: []*split.TrafficSplit{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "split1",
					},
					Spec: split.TrafficSplitSpec{
						Service: "s2-apex",
						Backends: []split.TrafficSplitBackend{
							{
								Service: "s1",
								Weight:  10,
							},
							{
								Service: "s-unused",
								Weight:  90,
							},
						},
					},
				},
			},
			prepare: func(mockK8s *k8s.MockController, trafficSplits []*split.TrafficSplit, trafficTargets []*access.TrafficTarget, upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting) {
				mockK8s.EXPECT().ListTrafficSplits().Return(trafficSplits).AnyTimes()
				mockK8s.EXPECT().ListMeshRootCertificates().Return(nil, nil).AnyTimes()
			},
			expectedInboundMeshHTTPRouteConfigsPerPort: map[int][]*trafficpolicy.InboundTrafficPolicy{
				80: {
					{
						Name: "s1.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1",
							"s1:80",
							"s1.ns1",
							"s1.ns1:80",
							"s1.ns1.svc",
							"s1.ns1.svc:80",
							"s1.ns1.svc.cluster",
							"s1.ns1.svc.cluster:80",
							"s1.ns1.svc.cluster.local",
							"s1.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.WildCardRouteMatch,
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.WildcardPrincipal),
							},
						},
					},
					{
						Name: "s2-apex.ns1.svc.cluster.local",
						Hostnames: []string{
							"s2-apex",
							"s2-apex:80",
							"s2-apex.ns1",
							"s2-apex.ns1:80",
							"s2-apex.ns1.svc",
							"s2-apex.ns1.svc:80",
							"s2-apex.ns1.svc.cluster",
							"s2-apex.ns1.svc.cluster:80",
							"s2-apex.ns1.svc.cluster.local",
							"s2-apex.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.WildCardRouteMatch,
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s2-apex|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.WildcardPrincipal),
							},
						},
					},
				},
			},
			expectedInboundMeshClusterConfigs: []*trafficpolicy.MeshClusterConfig{
				{
					Name:    "ns1/s1|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s2-apex|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s2-apex", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
			},
		},
		{
			name:             "multiple services, SMI mode, 1 TrafficTarget, 1 HTTPRouteGroup, 0 TrafficSplit, with local rate limiting",
			upstreamIdentity: upstreamSvcAccount.ToServiceIdentity(),
			upstreamServices: []service.MeshService{
				{
					Name:       "s1",
					Namespace:  "ns1",
					Port:       80,
					TargetPort: 8080,
					Protocol:   "http",
				},
				{
					Name:       "s2",
					Namespace:  "ns1",
					Port:       90,
					TargetPort: 9090,
					Protocol:   "http",
				},
			},
			permissiveMode: false,
			trafficTargets: []*access.TrafficTarget{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "access.smi-spec.io/v1alpha3",
						Kind:       "TrafficTarget",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "t1",
						Namespace: "ns1",
					},
					Spec: access.TrafficTargetSpec{
						Destination: access.IdentityBindingSubject{
							Kind:      "ServiceAccount",
							Name:      "sa1",
							Namespace: "ns1",
						},
						Sources: []access.IdentityBindingSubject{{
							Kind:      "ServiceAccount",
							Name:      "sa2",
							Namespace: "ns2",
						}},
						Rules: []access.TrafficTargetRule{{
							Kind:    "HTTPRouteGroup",
							Name:    "rule-1",
							Matches: []string{"route-1"},
						}},
					},
				},
			},
			httpRouteGroups: []*spec.HTTPRouteGroup{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "specs.smi-spec.io/v1alpha4",
						Kind:       "HTTPRouteGroup",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "rule-1",
					},
					Spec: spec.HTTPRouteGroupSpec{
						Matches: []spec.HTTPMatch{
							{
								Name:      "route-1",
								PathRegex: "/get",
								Methods:   []string{"GET"},
								Headers: map[string]string{
									"foo": "bar",
								},
							},
						},
					},
				},
			},
			trafficSplits: nil,
			upstreamTrafficSettings: []*policyv1alpha1.UpstreamTrafficSetting{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
					},
					Spec: policyv1alpha1.UpstreamTrafficSettingSpec{
						Host:      "s1.ns1.svc.cluster.local",
						RateLimit: virtualHostLocalRateLimitConfig,
						HTTPRoutes: []policyv1alpha1.HTTPRouteSpec{
							{
								Path:      "/get", // matches route allowed by HTTPRouteGroup
								RateLimit: perRouteLocalRateLimitConfig,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
					},
					Spec: policyv1alpha1.UpstreamTrafficSettingSpec{
						Host:      "s2.ns1.svc.cluster.local",
						RateLimit: virtualHostLocalRateLimitConfig,
						HTTPRoutes: []policyv1alpha1.HTTPRouteSpec{
							{
								Path:      "/get", // matches route allowed by HTTPRouteGroup
								RateLimit: perRouteLocalRateLimitConfig,
							},
						},
					},
				},
			},
			prepare: func(mockK8s *k8s.MockController, trafficSplits []*split.TrafficSplit, trafficTargets []*access.TrafficTarget, upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting) {
				mockK8s.EXPECT().ListTrafficSplits().Return(trafficSplits).AnyTimes()
				mockK8s.EXPECT().ListMeshRootCertificates().Return(nil, nil).AnyTimes()
			},
			expectedInboundMeshHTTPRouteConfigsPerPort: map[int][]*trafficpolicy.InboundTrafficPolicy{
				8080: {
					{
						Name: "s1.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1",
							"s1:80",
							"s1.ns1",
							"s1.ns1:80",
							"s1.ns1.svc",
							"s1.ns1.svc:80",
							"s1.ns1.svc.cluster",
							"s1.ns1.svc.cluster:80",
							"s1.ns1.svc.cluster.local",
							"s1.ns1.svc.cluster.local:80",
						},
						RateLimit: virtualHostLocalRateLimitConfig,
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1|8080|local",
										Weight:      100,
									}),
									RateLimit: perRouteLocalRateLimitConfig,
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false)),
							},
						},
					},
				},
				9090: {
					{
						Name: "s2.ns1.svc.cluster.local",
						Hostnames: []string{
							"s2",
							"s2:90",
							"s2.ns1",
							"s2.ns1:90",
							"s2.ns1.svc",
							"s2.ns1.svc:90",
							"s2.ns1.svc.cluster",
							"s2.ns1.svc.cluster:90",
							"s2.ns1.svc.cluster.local",
							"s2.ns1.svc.cluster.local:90",
						},
						RateLimit: virtualHostLocalRateLimitConfig,
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s2|9090|local",
										Weight:      100,
									}),
									RateLimit: perRouteLocalRateLimitConfig,
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false)),
							},
						},
					},
				},
			},
			expectedInboundMeshClusterConfigs: []*trafficpolicy.MeshClusterConfig{
				{
					Name:    "ns1/s1|8080|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1", Port: 80, TargetPort: 8080, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    8080,
				},
				{
					Name:    "ns1/s2|9090|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s2", Port: 90, TargetPort: 9090, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    9090,
				},
			},
		},
		{
			name:             "multiple services, permissive mode, 0 TrafficSplit, with local rate limiting",
			upstreamIdentity: upstreamSvcAccount.ToServiceIdentity(),
			upstreamServices: []service.MeshService{
				{
					Name:       "s1",
					Namespace:  "ns1",
					Port:       80,
					TargetPort: 80,
					Protocol:   "http",
				},
				{
					Name:       "s2",
					Namespace:  "ns1",
					Port:       90,
					TargetPort: 90,
					Protocol:   "http",
				},
			},
			permissiveMode: true,
			upstreamTrafficSettings: []*policyv1alpha1.UpstreamTrafficSetting{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
					},
					Spec: policyv1alpha1.UpstreamTrafficSettingSpec{
						Host:      "s1.ns1.svc.cluster.local",
						RateLimit: virtualHostLocalRateLimitConfig,
						HTTPRoutes: []policyv1alpha1.HTTPRouteSpec{
							{
								Path:      ".*", // matches wildcard path regex for permissive mode
								RateLimit: perRouteLocalRateLimitConfig,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
					},
					Spec: policyv1alpha1.UpstreamTrafficSettingSpec{
						Host:      "s2.ns1.svc.cluster.local",
						RateLimit: virtualHostLocalRateLimitConfig,
						HTTPRoutes: []policyv1alpha1.HTTPRouteSpec{
							{
								Path:      ".*", // matches wildcard path regex for permissive mode
								RateLimit: perRouteLocalRateLimitConfig,
							},
						},
					},
				},
			},
			prepare: func(mockK8s *k8s.MockController, trafficSplits []*split.TrafficSplit, trafficTargets []*access.TrafficTarget, upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting) {
				mockK8s.EXPECT().ListTrafficSplits().Return(trafficSplits).AnyTimes()
				mockK8s.EXPECT().ListMeshRootCertificates().Return(nil, nil).AnyTimes()
			},
			expectedInboundMeshHTTPRouteConfigsPerPort: map[int][]*trafficpolicy.InboundTrafficPolicy{
				80: {
					{
						Name: "s1.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1",
							"s1:80",
							"s1.ns1",
							"s1.ns1:80",
							"s1.ns1.svc",
							"s1.ns1.svc:80",
							"s1.ns1.svc.cluster",
							"s1.ns1.svc.cluster:80",
							"s1.ns1.svc.cluster.local",
							"s1.ns1.svc.cluster.local:80",
						},
						RateLimit: virtualHostLocalRateLimitConfig,
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.WildCardRouteMatch,
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1|80|local",
										Weight:      100,
									}),
									RateLimit: perRouteLocalRateLimitConfig,
								},
								AllowedPrincipals: mapset.NewSet(identity.WildcardPrincipal),
							},
						},
					},
				},
				90: {
					{
						Name: "s2.ns1.svc.cluster.local",
						Hostnames: []string{
							"s2",
							"s2:90",
							"s2.ns1",
							"s2.ns1:90",
							"s2.ns1.svc",
							"s2.ns1.svc:90",
							"s2.ns1.svc.cluster",
							"s2.ns1.svc.cluster:90",
							"s2.ns1.svc.cluster.local",
							"s2.ns1.svc.cluster.local:90",
						},
						RateLimit: virtualHostLocalRateLimitConfig,
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.WildCardRouteMatch,
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s2|90|local",
										Weight:      100,
									}),
									RateLimit: perRouteLocalRateLimitConfig,
								},
								AllowedPrincipals: mapset.NewSet(identity.WildcardPrincipal),
							},
						},
					},
				},
			},
			expectedInboundMeshClusterConfigs: []*trafficpolicy.MeshClusterConfig{
				{
					Name:    "ns1/s1|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s2|90|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s2", Port: 90, TargetPort: 90, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    90,
				},
			},
		},
		{
			name:             "multiple services, permissive mode, 0 TrafficSplit, with global rate limiting",
			upstreamIdentity: upstreamSvcAccount.ToServiceIdentity(),
			upstreamServices: []service.MeshService{
				{
					Name:       "s1",
					Namespace:  "ns1",
					Port:       80,
					TargetPort: 80,
					Protocol:   "http",
				},
				{
					Name:       "s2",
					Namespace:  "ns1",
					Port:       90,
					TargetPort: 90,
					Protocol:   "http",
				},
			},
			permissiveMode: true,
			upstreamTrafficSettings: []*policyv1alpha1.UpstreamTrafficSetting{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
					},
					Spec: policyv1alpha1.UpstreamTrafficSettingSpec{
						Host:      "s1.ns1.svc.cluster.local",
						RateLimit: virtualHostGlobalRateLimitConfig,
						HTTPRoutes: []policyv1alpha1.HTTPRouteSpec{
							{
								Path:      ".*", // matches wildcard path regex for permissive mode
								RateLimit: perRouteGlobalRateLimitConfig,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
					},
					Spec: policyv1alpha1.UpstreamTrafficSettingSpec{
						Host:      "s2.ns1.svc.cluster.local",
						RateLimit: virtualHostGlobalRateLimitConfig,
						HTTPRoutes: []policyv1alpha1.HTTPRouteSpec{
							{
								Path:      ".*", // matches wildcard path regex for permissive mode
								RateLimit: perRouteGlobalRateLimitConfig,
							},
						},
					},
				},
			},
			prepare: func(mockK8s *k8s.MockController, trafficSplits []*split.TrafficSplit, trafficTargets []*access.TrafficTarget, upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting) {
				mockK8s.EXPECT().ListTrafficSplits().Return(trafficSplits).AnyTimes()
				mockK8s.EXPECT().ListMeshRootCertificates().Return(nil, nil).AnyTimes()
			},
			expectedInboundMeshHTTPRouteConfigsPerPort: map[int][]*trafficpolicy.InboundTrafficPolicy{
				80: {
					{
						Name: "s1.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1",
							"s1:80",
							"s1.ns1",
							"s1.ns1:80",
							"s1.ns1.svc",
							"s1.ns1.svc:80",
							"s1.ns1.svc.cluster",
							"s1.ns1.svc.cluster:80",
							"s1.ns1.svc.cluster.local",
							"s1.ns1.svc.cluster.local:80",
						},
						RateLimit: virtualHostGlobalRateLimitConfig,
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.WildCardRouteMatch,
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1|80|local",
										Weight:      100,
									}),
									RateLimit: perRouteGlobalRateLimitConfig,
								},
								AllowedPrincipals: mapset.NewSet(identity.WildcardPrincipal),
							},
						},
					},
				},
				90: {
					{
						Name: "s2.ns1.svc.cluster.local",
						Hostnames: []string{
							"s2",
							"s2:90",
							"s2.ns1",
							"s2.ns1:90",
							"s2.ns1.svc",
							"s2.ns1.svc:90",
							"s2.ns1.svc.cluster",
							"s2.ns1.svc.cluster:90",
							"s2.ns1.svc.cluster.local",
							"s2.ns1.svc.cluster.local:90",
						},
						RateLimit: virtualHostGlobalRateLimitConfig,
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.WildCardRouteMatch,
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s2|90|local",
										Weight:      100,
									}),
									RateLimit: perRouteGlobalRateLimitConfig,
								},
								AllowedPrincipals: mapset.NewSet(identity.WildcardPrincipal),
							},
						},
					},
				},
			},
			expectedInboundMeshClusterConfigs: []*trafficpolicy.MeshClusterConfig{
				{
					Name:    "ns1/s1|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s2|90|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s2", Port: 90, TargetPort: 90, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    90,
				},
				{
					Name:     "foo.bar|8080",
					Address:  "foo.bar",
					Port:     8080,
					Protocol: constants.ProtocolH2C,
				},
			},
		},
		{
			name:             "multiple services, SMI mode, 1 TrafficTarget, 1 HTTPRouteGroup, 1 TrafficSplit and multiple trust domains",
			upstreamIdentity: upstreamSvcAccount.ToServiceIdentity(),
			upstreamServices: []service.MeshService{
				{
					Name:       "s1",
					Namespace:  "ns1",
					Port:       80,
					TargetPort: 80,
					Protocol:   "http",
				},
				{
					Name:       "s2",
					Namespace:  "ns1",
					Port:       90,
					TargetPort: 90,
					Protocol:   "http",
				},
			},
			permissiveMode: false,
			newTrustDomain: "cluster.new",
			trafficTargets: []*access.TrafficTarget{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "access.smi-spec.io/v1alpha3",
						Kind:       "TrafficTarget",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "t1",
						Namespace: "ns1",
					},
					Spec: access.TrafficTargetSpec{
						Destination: access.IdentityBindingSubject{
							Kind:      "ServiceAccount",
							Name:      "sa1",
							Namespace: "ns1",
						},
						Sources: []access.IdentityBindingSubject{{
							Kind:      "ServiceAccount",
							Name:      "sa2",
							Namespace: "ns2",
						}},
						Rules: []access.TrafficTargetRule{{
							Kind:    "HTTPRouteGroup",
							Name:    "rule-1",
							Matches: []string{"route-1"},
						}},
					},
				},
			},
			httpRouteGroups: []*spec.HTTPRouteGroup{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "specs.smi-spec.io/v1alpha4",
						Kind:       "HTTPRouteGroup",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "rule-1",
					},
					Spec: spec.HTTPRouteGroupSpec{
						Matches: []spec.HTTPMatch{
							{
								Name:      "route-1",
								PathRegex: "/get",
								Methods:   []string{"GET"},
								Headers: map[string]string{
									"foo": "bar",
								},
							},
						},
					},
				},
			},
			trafficSplits: []*split.TrafficSplit{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "split1",
					},
					Spec: split.TrafficSplitSpec{
						Service: "s1-apex",
						Backends: []split.TrafficSplitBackend{
							{
								Service: "s1",
								Weight:  10,
							},
							{
								Service: "s-unused",
								Weight:  90,
							},
						},
					},
				},
			},
			prepare: func(mockK8s *k8s.MockController, trafficSplits []*split.TrafficSplit, trafficTargets []*access.TrafficTarget, upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting) {
				mockK8s.EXPECT().ListTrafficSplits().Return(trafficSplits).AnyTimes()
			},
			expectedInboundMeshHTTPRouteConfigsPerPort: map[int][]*trafficpolicy.InboundTrafficPolicy{
				80: {
					{
						Name: "s1.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1",
							"s1:80",
							"s1.ns1",
							"s1.ns1:80",
							"s1.ns1.svc",
							"s1.ns1.svc:80",
							"s1.ns1.svc.cluster",
							"s1.ns1.svc.cluster:80",
							"s1.ns1.svc.cluster.local",
							"s1.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false), identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.new", false)),
							},
						},
					},
					{
						Name: "s1-apex.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1-apex",
							"s1-apex:80",
							"s1-apex.ns1",
							"s1-apex.ns1:80",
							"s1-apex.ns1.svc",
							"s1-apex.ns1.svc:80",
							"s1-apex.ns1.svc.cluster",
							"s1-apex.ns1.svc.cluster:80",
							"s1-apex.ns1.svc.cluster.local",
							"s1-apex.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1-apex|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false), identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.new", false)),
							},
						},
					},
				},
				90: {
					{
						Name: "s2.ns1.svc.cluster.local",
						Hostnames: []string{
							"s2",
							"s2:90",
							"s2.ns1",
							"s2.ns1:90",
							"s2.ns1.svc",
							"s2.ns1.svc:90",
							"s2.ns1.svc.cluster",
							"s2.ns1.svc.cluster:90",
							"s2.ns1.svc.cluster.local",
							"s2.ns1.svc.cluster.local:90",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s2|90|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", false), identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.new", false)),
							},
						},
					},
				},
			},
			expectedInboundMeshClusterConfigs: []*trafficpolicy.MeshClusterConfig{
				{
					Name:    "ns1/s1|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s1-apex|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1-apex", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s2|90|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s2", Port: 90, TargetPort: 90, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    90,
				},
			},
		},
		{
			name:             "multiple services, SMI mode, 1 TrafficTarget, 1 HTTPRouteGroup, 1 TrafficSplit and spiffe enabled",
			spiffeEnabled:    true,
			upstreamIdentity: upstreamSvcAccount.ToServiceIdentity(),
			upstreamServices: []service.MeshService{
				{
					Name:       "s1",
					Namespace:  "ns1",
					Port:       80,
					TargetPort: 80,
					Protocol:   "http",
				},
				{
					Name:       "s2",
					Namespace:  "ns1",
					Port:       90,
					TargetPort: 90,
					Protocol:   "http",
				},
			},
			permissiveMode: false,
			trafficTargets: []*access.TrafficTarget{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "access.smi-spec.io/v1alpha3",
						Kind:       "TrafficTarget",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "t1",
						Namespace: "ns1",
					},
					Spec: access.TrafficTargetSpec{
						Destination: access.IdentityBindingSubject{
							Kind:      "ServiceAccount",
							Name:      "sa1",
							Namespace: "ns1",
						},
						Sources: []access.IdentityBindingSubject{{
							Kind:      "ServiceAccount",
							Name:      "sa2",
							Namespace: "ns2",
						}},
						Rules: []access.TrafficTargetRule{{
							Kind:    "HTTPRouteGroup",
							Name:    "rule-1",
							Matches: []string{"route-1"},
						}},
					},
				},
			},
			httpRouteGroups: []*spec.HTTPRouteGroup{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "specs.smi-spec.io/v1alpha4",
						Kind:       "HTTPRouteGroup",
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "rule-1",
					},
					Spec: spec.HTTPRouteGroupSpec{
						Matches: []spec.HTTPMatch{
							{
								Name:      "route-1",
								PathRegex: "/get",
								Methods:   []string{"GET"},
								Headers: map[string]string{
									"foo": "bar",
								},
							},
						},
					},
				},
			},
			trafficSplits: []*split.TrafficSplit{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "ns1",
						Name:      "split1",
					},
					Spec: split.TrafficSplitSpec{
						Service: "s1-apex",
						Backends: []split.TrafficSplitBackend{
							{
								Service: "s1",
								Weight:  10,
							},
							{
								Service: "s-unused",
								Weight:  90,
							},
						},
					},
				},
			},
			prepare: func(mockK8s *k8s.MockController, trafficSplits []*split.TrafficSplit, trafficTargets []*access.TrafficTarget, upstreamTrafficSettings []*policyv1alpha1.UpstreamTrafficSetting) {
				mockK8s.EXPECT().ListTrafficSplits().Return(trafficSplits).AnyTimes()
			},
			expectedInboundMeshHTTPRouteConfigsPerPort: map[int][]*trafficpolicy.InboundTrafficPolicy{
				80: {
					{
						Name: "s1.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1",
							"s1:80",
							"s1.ns1",
							"s1.ns1:80",
							"s1.ns1.svc",
							"s1.ns1.svc:80",
							"s1.ns1.svc.cluster",
							"s1.ns1.svc.cluster:80",
							"s1.ns1.svc.cluster.local",
							"s1.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", true)),
							},
						},
					},
					{
						Name: "s1-apex.ns1.svc.cluster.local",
						Hostnames: []string{
							"s1-apex",
							"s1-apex:80",
							"s1-apex.ns1",
							"s1-apex.ns1:80",
							"s1-apex.ns1.svc",
							"s1-apex.ns1.svc:80",
							"s1-apex.ns1.svc.cluster",
							"s1-apex.ns1.svc.cluster:80",
							"s1-apex.ns1.svc.cluster.local",
							"s1-apex.ns1.svc.cluster.local:80",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s1-apex|80|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", true)),
							},
						},
					},
				},
				90: {
					{
						Name: "s2.ns1.svc.cluster.local",
						Hostnames: []string{
							"s2",
							"s2:90",
							"s2.ns1",
							"s2.ns1:90",
							"s2.ns1.svc",
							"s2.ns1.svc:90",
							"s2.ns1.svc.cluster",
							"s2.ns1.svc.cluster:90",
							"s2.ns1.svc.cluster.local",
							"s2.ns1.svc.cluster.local:90",
						},
						Rules: []*trafficpolicy.Rule{
							{
								Route: trafficpolicy.RouteWeightedClusters{
									HTTPRouteMatch: trafficpolicy.HTTPRouteMatch{
										Path:          "/get",
										PathMatchType: trafficpolicy.PathMatchRegex,
										Methods:       []string{"GET"},
										Headers: map[string]string{
											"foo": "bar",
										},
									},
									WeightedClusters: mapset.NewSet(service.WeightedCluster{
										ClusterName: "ns1/s2|90|local",
										Weight:      100,
									}),
								},
								AllowedPrincipals: mapset.NewSet(identity.K8sServiceAccount{
									Name:      "sa2",
									Namespace: "ns2",
								}.AsPrincipal("cluster.local", true)),
							},
						},
					},
				},
			},
			expectedInboundMeshClusterConfigs: []*trafficpolicy.MeshClusterConfig{
				{
					Name:    "ns1/s1|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s1-apex|80|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s1-apex", Port: 80, TargetPort: 80, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    80,
				},
				{
					Name:    "ns1/s2|90|local",
					Service: service.MeshService{Namespace: "ns1", Name: "s2", Port: 90, TargetPort: 90, Protocol: "http"},
					Address: "127.0.0.1",
					Port:    90,
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert := tassert.New(t)
			mockCtrl := gomock.NewController(t)
			defer mockCtrl.Finish()

			mrc1 := &v1alpha2.MeshRootCertificate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "osm-mesh-root-certificate",
					Namespace: "osm-system",
				},
				Spec: v1alpha2.MeshRootCertificateSpec{
					TrustDomain:   "cluster.local",
					Intent:        v1alpha2.ActiveIntent,
					SpiffeEnabled: tc.spiffeEnabled,
				},
			}

			configClient := configFake.NewSimpleClientset([]runtime.Object{mrc1}...)
			mrcClient := tresorFake.NewFakeMRCWithConfig(configClient)
			fakeCertManager := tresorFake.NewFakeWithMRCClient(mrcClient, 1*time.Hour)
			mockK8s := k8s.NewMockController(mockCtrl)
			computeClient := kube.NewClient(mockK8s)

			mrcClient.NewCertEvent(mrc1.Name)

			mc := MeshCatalog{
				certManager: fakeCertManager,
				Interface:   computeClient,
			}

			mockK8s.EXPECT().ListUpstreamTrafficSettings().Return(tc.upstreamTrafficSettings).AnyTimes()
			mockK8s.EXPECT().ListEgressPolicies().Return([]*policyv1alpha1.Egress{}).AnyTimes()
			mockK8s.EXPECT().GetMeshConfig().Return(v1alpha2.MeshConfig{
				Spec: v1alpha2.MeshConfigSpec{
					Traffic: v1alpha2.TrafficSpec{
						EnablePermissiveTrafficPolicyMode: tc.permissiveMode,
					},
				},
			}).AnyTimes()
			mockK8s.EXPECT().ListTrafficTargets().Return(tc.trafficTargets).AnyTimes()
			mockK8s.EXPECT().ListHTTPTrafficSpecs().Return(tc.httpRouteGroups).AnyTimes()
			tc.prepare(mockK8s, tc.trafficSplits, tc.trafficTargets, tc.upstreamTrafficSettings)

			if tc.newTrustDomain != "" {
				// create a new MRC with the newTrustDomain
				mrc2 := &v1alpha2.MeshRootCertificate{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "osm-mesh-root-certificate-2",
						Namespace: "osm-system",
					},
					Spec: v1alpha2.MeshRootCertificateSpec{
						TrustDomain: tc.newTrustDomain,
						Intent:      v1alpha2.ActiveIntent,
					},
				}
				_, err := configClient.ConfigV1alpha2().MeshRootCertificates("osm-system").Create(context.Background(), mrc2, metav1.CreateOptions{})
				assert.NoError(err)

				// generate an MRCEvent for the new MRC to update the issuers and trigger a rotation
				mrcClient.NewCertEvent(mrc2.Name)
				assert.Eventually(func() bool {
					return fakeCertManager.GetIssuersInfo().AreDifferent()
				}, 2*time.Second, 100*time.Millisecond)
			}

			actualClusterConfigs := mc.GetInboundMeshClusterConfigs(tc.upstreamServices)
			actualHTTPRouteConfigsPerPort := mc.GetInboundMeshHTTPRouteConfigsPerPort(tc.upstreamIdentity, tc.upstreamServices)
			actualTrafficMatches := mc.GetInboundMeshTrafficMatches(tc.upstreamServices)

			// Verify expected fields
			assert.ElementsMatch(tc.expectedInboundMeshClusterConfigs, actualClusterConfigs)
			for expectedKey, expectedVal := range tc.expectedInboundMeshHTTPRouteConfigsPerPort {
				assert.ElementsMatch(expectedVal, actualHTTPRouteConfigsPerPort[expectedKey])
			}
			if len(tc.expectedInboundMeshTrafficMatches) != 0 {
				assert.ElementsMatch(tc.expectedInboundMeshTrafficMatches, actualTrafficMatches)
			}
		})
	}
}

func TestRoutesFromRules(t *testing.T) {
	assert := tassert.New(t)

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	mockK8s := k8s.NewMockController(mockCtrl)
	provider := kube.NewClient(mockK8s)

	mockK8s.EXPECT().ListHTTPTrafficSpecs().Return([]*spec.HTTPRouteGroup{&tests.HTTPRouteGroup}).AnyTimes()

	mc := MeshCatalog{Interface: provider}

	testCases := []struct {
		name           string
		rules          []access.TrafficTargetRule
		namespace      string
		expectedRoutes []trafficpolicy.HTTPRouteMatch
	}{
		{
			name: "http route group and match name exist",
			rules: []access.TrafficTargetRule{
				{
					Kind:    "HTTPRouteGroup",
					Name:    tests.RouteGroupName,
					Matches: []string{tests.BuyBooksMatchName},
				},
			},
			namespace:      tests.Namespace,
			expectedRoutes: []trafficpolicy.HTTPRouteMatch{tests.BookstoreBuyHTTPRoute},
		},
		{
			name: "http route group and match name do not exist",
			rules: []access.TrafficTargetRule{
				{
					Kind:    "HTTPRouteGroup",
					Name:    "DoesNotExist",
					Matches: []string{"hello"},
				},
			},
			namespace:      tests.Namespace,
			expectedRoutes: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("Testing routesFromRules where %s", tc.name), func(t *testing.T) {
			routes, err := mc.routesFromRules(tc.rules, tc.namespace)
			assert.Nil(err)
			assert.EqualValues(tc.expectedRoutes, routes)
		})
	}
}

func TestGetHTTPPathsPerRoute(t *testing.T) {
	assert := tassert.New(t)

	testCases := []struct {
		name                      string
		trafficSpec               spec.HTTPRouteGroup
		expectedHTTPPathsPerRoute map[trafficpolicy.TrafficSpecName]map[trafficpolicy.TrafficSpecMatchName]trafficpolicy.HTTPRouteMatch
	}{
		{
			name: "HTTP route with path, method and headers",
			trafficSpec: spec.HTTPRouteGroup{
				TypeMeta: v1.TypeMeta{
					APIVersion: "specs.smi-spec.io/v1alpha4",
					Kind:       "HTTPRouteGroup",
				},
				ObjectMeta: v1.ObjectMeta{
					Namespace: "default",
					Name:      tests.RouteGroupName,
				},

				Spec: spec.HTTPRouteGroupSpec{
					Matches: []spec.HTTPMatch{
						{
							Name:      tests.BuyBooksMatchName,
							PathRegex: tests.BookstoreBuyPath,
							Methods:   []string{"GET"},
							Headers: map[string]string{
								"user-agent": tests.HTTPUserAgent,
							},
						},
						{
							Name:      tests.SellBooksMatchName,
							PathRegex: tests.BookstoreSellPath,
							Methods:   []string{"GET"},
							Headers: map[string]string{
								"user-agent": tests.HTTPUserAgent,
							},
						},
					},
				},
			},
			expectedHTTPPathsPerRoute: map[trafficpolicy.TrafficSpecName]map[trafficpolicy.TrafficSpecMatchName]trafficpolicy.HTTPRouteMatch{
				"HTTPRouteGroup/default/bookstore-service-routes": {
					trafficpolicy.TrafficSpecMatchName(tests.BuyBooksMatchName): {
						Path:          tests.BookstoreBuyPath,
						PathMatchType: trafficpolicy.PathMatchRegex,
						Methods:       []string{"GET"},
						Headers: map[string]string{
							"user-agent": tests.HTTPUserAgent,
						},
					},
					trafficpolicy.TrafficSpecMatchName(tests.SellBooksMatchName): {
						Path:          tests.BookstoreSellPath,
						PathMatchType: trafficpolicy.PathMatchRegex,
						Methods:       []string{"GET"},
						Headers: map[string]string{
							"user-agent": tests.HTTPUserAgent,
						},
					},
				},
			},
		},
		{
			name: "HTTP route with only path",
			trafficSpec: spec.HTTPRouteGroup{
				TypeMeta: v1.TypeMeta{
					APIVersion: "specs.smi-spec.io/v1alpha4",
					Kind:       "HTTPRouteGroup",
				},
				ObjectMeta: v1.ObjectMeta{
					Namespace: "default",
					Name:      tests.RouteGroupName,
				},

				Spec: spec.HTTPRouteGroupSpec{
					Matches: []spec.HTTPMatch{
						{
							Name:      tests.BuyBooksMatchName,
							PathRegex: tests.BookstoreBuyPath,
						},
						{
							Name:      tests.SellBooksMatchName,
							PathRegex: tests.BookstoreSellPath,
							Methods:   nil,
						},
					},
				},
			},
			expectedHTTPPathsPerRoute: map[trafficpolicy.TrafficSpecName]map[trafficpolicy.TrafficSpecMatchName]trafficpolicy.HTTPRouteMatch{
				"HTTPRouteGroup/default/bookstore-service-routes": {
					trafficpolicy.TrafficSpecMatchName(tests.BuyBooksMatchName): {
						Path:          tests.BookstoreBuyPath,
						PathMatchType: trafficpolicy.PathMatchRegex,
						Methods:       []string{"*"},
					},
					trafficpolicy.TrafficSpecMatchName(tests.SellBooksMatchName): {
						Path:          tests.BookstoreSellPath,
						PathMatchType: trafficpolicy.PathMatchRegex,
						Methods:       []string{"*"},
					},
				},
			},
		},
		{
			name: "HTTP route with only method",
			trafficSpec: spec.HTTPRouteGroup{
				TypeMeta: v1.TypeMeta{
					APIVersion: "specs.smi-spec.io/v1alpha4",
					Kind:       "HTTPRouteGroup",
				},
				ObjectMeta: v1.ObjectMeta{
					Namespace: "default",
					Name:      tests.RouteGroupName,
				},

				Spec: spec.HTTPRouteGroupSpec{
					Matches: []spec.HTTPMatch{
						{
							Name:    tests.BuyBooksMatchName,
							Methods: []string{"GET"},
						},
					},
				},
			},
			expectedHTTPPathsPerRoute: map[trafficpolicy.TrafficSpecName]map[trafficpolicy.TrafficSpecMatchName]trafficpolicy.HTTPRouteMatch{
				"HTTPRouteGroup/default/bookstore-service-routes": {
					trafficpolicy.TrafficSpecMatchName(tests.BuyBooksMatchName): {
						Path:    ".*",
						Methods: []string{"GET"},
					},
				},
			},
		},
		{
			name: "HTTP route with only headers",
			trafficSpec: spec.HTTPRouteGroup{
				TypeMeta: v1.TypeMeta{
					APIVersion: "specs.smi-spec.io/v1alpha4",
					Kind:       "HTTPRouteGroup",
				},
				ObjectMeta: v1.ObjectMeta{
					Namespace: "default",
					Name:      tests.RouteGroupName,
				},

				Spec: spec.HTTPRouteGroupSpec{
					Matches: []spec.HTTPMatch{
						{
							Name: tests.WildcardWithHeadersMatchName,
							Headers: map[string]string{
								"user-agent": tests.HTTPUserAgent,
							},
						},
					},
				},
			},
			expectedHTTPPathsPerRoute: map[trafficpolicy.TrafficSpecName]map[trafficpolicy.TrafficSpecMatchName]trafficpolicy.HTTPRouteMatch{
				"HTTPRouteGroup/default/bookstore-service-routes": {
					trafficpolicy.TrafficSpecMatchName(tests.WildcardWithHeadersMatchName): {
						Path:          ".*",
						PathMatchType: trafficpolicy.PathMatchRegex,
						Methods:       []string{"*"},
						Headers: map[string]string{
							"user-agent": tests.HTTPUserAgent,
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockCtrl := gomock.NewController(t)
			defer mockCtrl.Finish()
			mockK8s := k8s.NewMockController(mockCtrl)
			provider := kube.NewClient(mockK8s)

			mc := MeshCatalog{
				Interface: provider,
			}

			mockK8s.EXPECT().ListHTTPTrafficSpecs().Return([]*spec.HTTPRouteGroup{&tc.trafficSpec}).AnyTimes()
			actual, err := mc.getHTTPPathsPerRoute()
			assert.Nil(err)
			assert.True(reflect.DeepEqual(actual, tc.expectedHTTPPathsPerRoute))
		})
	}
}

func TestGetTrafficSpecName(t *testing.T) {
	assert := tassert.New(t)

	actual := getTrafficSpecName("HTTPRouteGroup", tests.Namespace, tests.RouteGroupName)
	expected := trafficpolicy.TrafficSpecName(fmt.Sprintf("HTTPRouteGroup/%s/%s", tests.Namespace, tests.RouteGroupName))
	assert.Equal(actual, expected)
}
