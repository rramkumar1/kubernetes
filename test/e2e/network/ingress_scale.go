/*
Copyright 2015 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package network

import (
	"fmt"
	"path/filepath"
	"strconv"
	"sync"

	rbacv1beta1 "k8s.io/api/rbac/v1beta1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/kubernetes/test/e2e/framework"

	. "github.com/onsi/ginkgo"
)

const (
	numIngresses = 2
)

var _ = SIGDescribe("Loadbalancing: L7", func() {
	defer GinkgoRecover()
	var (
		ns          string
		jigs        []*framework.IngressTestJig
		cloudConfig framework.CloudConfig
	)
	f := framework.NewDefaultFramework("ingress")

	BeforeEach(func() {
		for i := 0; i < numIngresses; i++ {
			jigs = append(jigs, framework.NewIngressTestJig(f.ClientSet))
		}
		ns = f.Namespace.Name
		cloudConfig = framework.TestContext.CloudConfig

		// this test wants powerful permissions.  Since the namespace names are unique, we can leave this
		// lying around so we don't have to race any caches
		framework.BindClusterRole(jigs[0].Client.RbacV1beta1(), "cluster-admin", f.Namespace.Name,
			rbacv1beta1.Subject{Kind: rbacv1beta1.ServiceAccountKind, Namespace: f.Namespace.Name, Name: "default"})

		err := framework.WaitForAuthorizationUpdate(jigs[0].Client.AuthorizationV1beta1(),
			serviceaccount.MakeUsername(f.Namespace.Name, "default"),
			"", "create", schema.GroupResource{Resource: "pods"}, true)
		framework.ExpectNoError(err)
	})
	Describe("GCE [Slow] [Feature:IngressFoo] [ScaleTest]", func() {
		var gceController *framework.GCEIngressController

		// Platform specific setup
		BeforeEach(func() {
			framework.SkipUnlessProviderIs("gce", "gke")
			By("Initializing gce controller")
			gceController = &framework.GCEIngressController{
				Ns: ns,
				// Each jig gets the same client so it does not matter which one we use.
				Client: jigs[0].Client,
				Cloud:  framework.TestContext.CloudConfig,
			}
			gceController.Init()
		})

		// Platform specific cleanup
		AfterEach(func() {
			if CurrentGinkgoTestDescription().Failed {
				framework.DescribeIng(ns)
			}
			c := make(chan bool, numIngresses)
			for i := 0; i < numIngresses; i++ {
				go func(idx int) {
					if jigs[idx].Ingress == nil {
						By("No ingress created, no cleanup necessary")
						return
					}
					By("Deleting ingress")
					jigs[idx].TryDeleteIngress()
					c <- true
				}(i)
				By("Cleaning up cloud resources")
				framework.CleanupGCEIngressController(gceController)
			}
			for len(c) != numIngresses {
				// TODO(rramkumar): Better way than just spinning until condition is met?
				// Wait until all ingresses are created
			}
		})

		It("should create 100 ingresses", func() {
			var wg sync.WaitGroup
			for i := 0; i < numIngresses; i++ {
				ip := gceController.CreateStaticIP(ns + "-" + strconv.Itoa(i))
				By(fmt.Sprintf("allocated static ip %v: %v through the GCE cloud provider", ns, ip))
				createService := false
				if i == 0 {
					createService = true
				}
				jigs[i].CreateIngress(filepath.Join(framework.IngressManifestPath, "static-ip-2"), ns, map[string]string{
					"kubernetes.io/ingress.global-static-ip-name": ns,
					"kubernetes.io/ingress.allow-http":            "false",
				}, map[string]string{}, createService, "static-ip-"+strconv.Itoa(i))
				jigs[i].AddHTTPS("tls-secret-"+strconv.Itoa(i), "ingress.test.com")
				// Waiting for ingress to come up takes a while so parallelize it.
				wg.Add(1)
				go func() {
					By("waiting for Ingress to come up with ip: " + ip)
					defer wg.Done()
					httpClient := framework.BuildInsecureClient(framework.IngressReqTimeout)
					framework.ExpectNoError(framework.PollURL(fmt.Sprintf("https://%v/", ip), "", framework.LoadBalancerPollTimeout, jigs[0].PollInterval, httpClient, false))
				}()
			}
			// Wait until all ingresses are up.
			wg.Wait()
		})
	})
})
