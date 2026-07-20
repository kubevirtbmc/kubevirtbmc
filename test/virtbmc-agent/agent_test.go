package virtbmcagent

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	"kubevirt.io/kubevirtbmc/test/util"
)

// Agent e2e tests run in order: IPMI first, then Redfish, then Virtual Media.

var _ = Describe("Agent e2e", Ordered, func() {
	var (
		env       *agentTestEnv
		ctx       context.Context
		ns        string
		authToken string
		err       error
	)

	BeforeAll(func() {
		ctx = context.Background()
		ns = agentNamespace
		env, err = ensureAgentTestEnv(ctx, ns, k8sClient)
		Expect(err).NotTo(HaveOccurred())

		clientset, err := kubernetes.NewForConfig(config)
		Expect(err).NotTo(HaveOccurred())
		Expect(CreateIPMIToolPod(ctx, clientset, ns)).To(Succeed())
		Expect(CreateRedfishClientPod(ctx, clientset, ns)).To(Succeed())
	})

	ipmiReq := func(args ...string) IPMIRequest {
		return IPMIRequest{
			ServiceHost: env.ServiceHost,
			Username:    env.Username,
			Password:    env.Password,
			Args:        args,
		}
	}
	redfishBasic := func(method, path, body string) RedfishRequest {
		return RedfishRequest{
			BaseURL:  env.RedfishBaseURL,
			Method:   method,
			Path:     path,
			Body:     body,
			Username: env.Username,
			Password: env.Password,
		}
	}
	redfishSession := func(method, path, body string) RedfishRequest {
		return RedfishRequest{
			BaseURL:    env.RedfishBaseURL,
			Method:     method,
			Path:       path,
			Body:       body,
			XAuthToken: authToken,
		}
	}

	Context("IPMI enable/disable toggle", func() {
		It("should start with IPMI disabled by default, verify failure, then enable", func() {
			By("verifying IPMI commands fail when disabled by default")
			_, _, err := runIPMIInCluster(ctx, config, ns, ipmiReq("power", "status"))
			Expect(err).To(HaveOccurred(), "IPMI command should fail when IPMI is disabled")

			By("recording the current pod UID before enabling IPMI")
			var podBefore corev1.Pod
			Expect(k8sClient.Get(ctx, util.AgentPodKey(ns), &podBefore)).To(Succeed())

			By("enabling IPMI on the VirtualMachineBMC")
			bmc := &bmcv1.VirtualMachineBMC{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: agentBMCName}, bmc)).To(Succeed())
			enabled := true
			bmc.Spec.IPMI = &bmcv1.IPMISpec{Enabled: &enabled}
			Expect(k8sClient.Update(ctx, bmc)).To(Succeed())

			By("waiting for new agent pod to be recreated and ready")
			Eventually(util.PodRunningAndReadyWithNewUID(ctx, k8sClient, ns, podBefore.UID), agentTestTimeout, agentTestInterval).Should(BeTrue(), "new agent pod should become ready")

			By("creating a new Redfish session for the restarted pod")
			Eventually(func() error {
				authToken, err = CreateRedfishSession(ctx, config, ns, env.RedfishBaseURL, env.Username, env.Password)
				return err
			}, agentTestTimeout, agentTestInterval).Should(Succeed())
			Expect(authToken).NotTo(BeEmpty())
		})
	})

	Context("IPMI operations", func() {
		Context("Authentication", func() {
			It("should accept commands with correct username and password", func() {
				out, _, err := runIPMIInCluster(ctx, config, ns, ipmiReq("power", "status"))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(SatisfyAny(
					ContainSubstring("Chassis Power is on"),
					ContainSubstring("Chassis Power is off"),
				))
			})

			It("should reject commands with wrong password", func() {
				wrongReq := ipmiReq("power", "status")
				wrongReq.Password = "wrongpass"
				_, stderr, err := runIPMIInCluster(ctx, config, ns, wrongReq)
				Expect(err).To(HaveOccurred(), "IPMI command with wrong password should be rejected")
				Expect(stderr).To(ContainSubstring("Unable to establish IPMI v2 / RMCP+ session"))
			})

			It("should reject commands with wrong username", func() {
				wrongReq := ipmiReq("power", "status")
				wrongReq.Username = "baduser"
				_, stderr, err := runIPMIInCluster(ctx, config, ns, wrongReq)
				Expect(err).To(HaveOccurred(), "IPMI command with wrong username should be rejected")
				Expect(stderr).To(ContainSubstring("Unable to establish IPMI v2 / RMCP+ session"))
			})

		})

		Context("Dual-stack (lan + lanplus)", func() {
			DescribeTable("should report power status",
				func(iface string) {
					req := ipmiReq("power", "status")
					req.Interface = iface
					out, _, err := runIPMIInCluster(ctx, config, ns, req)
					Expect(err).NotTo(HaveOccurred())
					Expect(out).To(SatisfyAny(
						ContainSubstring("Chassis Power is on"),
						ContainSubstring("Chassis Power is off"),
					))
				},
				Entry("via lan", "lan"),
				Entry("via lanplus", "lanplus"),
			)

			DescribeTable("should accept disable boot timeout raw command",
				func(iface string) {
					req := ipmiReq("raw", "0x00", "0x08", "0x03", "0x08")
					req.Interface = iface
					_, _, err := runIPMIInCluster(ctx, config, ns, req)
					Expect(err).NotTo(HaveOccurred())
				},
				Entry("via lan", "lan"),
				Entry("via lanplus", "lanplus"),
			)

			DescribeTable("should return power status within 1s with -R 1",
				func(iface string) {
					req := ipmiReq("power", "status")
					req.Interface = iface
					req.RetryCount = 1
					_, _, elapsed, err := runIPMIInClusterTimed(ctx, config, ns, req)
					Expect(err).NotTo(HaveOccurred())
					Expect(elapsed).To(BeNumerically("<", time.Second),
						"%s power status with -R 1 should return within 1s, took %v", iface, elapsed)
				},
				Entry("via lan", "lan"),
				Entry("via lanplus", "lanplus"),
			)
		})

		Context("Power management", func() {
			It("should report power status", func() {
				out, _, err := runIPMIInCluster(ctx, config, ns, ipmiReq("power", "status"))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(SatisfyAny(
					ContainSubstring("Chassis Power is on"),
					ContainSubstring("Chassis Power is off"),
				))
			})

			It("should accept power off and VM is actually off", func() {
				_, _, err := runIPMIInCluster(ctx, config, ns, ipmiReq("power", "off"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIDeleted(ctx, k8sClient, ns)
			})

			It("should accept power on and VM is actually running", func() {
				_, _, err := runIPMIInCluster(ctx, config, ns, ipmiReq("power", "on"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIRunning(ctx, k8sClient, ns)
			})

			It("should accept power cycle and VM is stopped then started again", func() {
				_, _, err := runIPMIInCluster(ctx, config, ns, ipmiReq("power", "cycle"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIPowerCycle(ctx, k8sClient, ns)
			})

			It("should accept power soft (graceful ACPI shutdown) and VM is actually off", func() {
				_, _, err := runIPMIInCluster(ctx, config, ns, ipmiReq("power", "soft"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIDeleted(ctx, k8sClient, ns)
			})

			It("should accept power on and VM is actually running", func() {
				_, _, err := runIPMIInCluster(ctx, config, ns, ipmiReq("power", "on"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIRunning(ctx, k8sClient, ns)
			})

			It("should accept power reset (hard reset) and VM is stopped then started again", func() {
				_, _, err := runIPMIInCluster(ctx, config, ns, ipmiReq("power", "reset"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIPowerCycle(ctx, k8sClient, ns)
			})
		})

		Context("Boot device configuration", func() {
			It("should set boot device to PXE", func() {
				out, _, err := runIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "pxe"))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(SatisfyAny(
					ContainSubstring("Set Boot Device"),
					ContainSubstring("Boot"),
					Equal(""),
				))
				// PXE: interfaces first, then regular disks, then cdroms
				verifyVMBootOrder(ctx, k8sClient, ns,
					map[int]uint{0: 2, 1: 3}, // disks: regular=2, cdrom=3
					map[int]uint{0: 1},       // interface=1
				)
			})

			It("should set boot device to disk", func() {
				out, _, err := runIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "disk"))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(SatisfyAny(
					ContainSubstring("Set Boot Device"),
					ContainSubstring("Boot"),
					Equal(""),
				))
				// HDD: regular disks first, then interfaces, then cdroms
				verifyVMBootOrder(ctx, k8sClient, ns,
					map[int]uint{0: 1, 1: 3}, // disks: regular=1, cdrom=3
					map[int]uint{0: 2},       // interface=2
				)
			})

			It("should set boot device to cdrom", func() {
				out, _, err := runIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "cdrom"))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(SatisfyAny(
					ContainSubstring("Set Boot Device"),
					ContainSubstring("Boot"),
					Equal(""),
				))
				// CD: cdroms first, then regular disks, then interfaces
				verifyVMBootOrder(ctx, k8sClient, ns,
					map[int]uint{0: 2, 1: 1}, // disks: regular=2, cdrom=1
					map[int]uint{0: 3},       // interface=3
				)
			})
		})
	})

	Context("Redfish operations", func() {
		Context("Authentication", func() {
			It("should allow access with basic auth", func() {
				out, err := runCurlRedfish(ctx, config, ns, redfishBasic("GET", "/Systems/1", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring(`"@odata.id":"/redfish/v1/Systems/1"`))
			})

			It("should reject access with wrong password", func() {
				out, err := runCurlRedfish(ctx, config, ns, RedfishRequest{
					BaseURL:  env.RedfishBaseURL,
					Method:   "GET",
					Path:     "/Systems/1",
					Username: env.Username,
					Password: "wrongpass",
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(And(
					ContainSubstring("401"),
					ContainSubstring("Unauthorized"),
				))
			})

			It("should reject access with wrong username", func() {
				out, err := runCurlRedfish(ctx, config, ns, RedfishRequest{
					BaseURL:  env.RedfishBaseURL,
					Method:   "GET",
					Path:     "/Systems/1",
					Username: "baduser",
					Password: env.Password,
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(And(
					ContainSubstring("401"),
					ContainSubstring("Unauthorized"),
				))
			})
		})

		Context("Service discovery", func() {
			It("should return service root at /redfish/v1", func() {
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("GET", "", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring(`"@odata.id":"/redfish/v1"`))
				Expect(out).To(ContainSubstring("RedfishVersion"))
				Expect(out).To(ContainSubstring("Systems"))
				Expect(out).To(ContainSubstring("Managers"))
			})
		})

		Context("Power management", func() {
			It("should return system power state", func() {
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("GET", "/Systems/1", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(SatisfyAny(
					ContainSubstring("On"),
					ContainSubstring("Off"),
				))
			})

			It("should advertise supported reset action values", func() {
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("GET", "/Systems/1", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring(`"ResetType@Redfish.AllowableValues"`))
				Expect(out).To(ContainSubstring(`"On"`))
				Expect(out).To(ContainSubstring(`"ForceOff"`))
				Expect(out).To(ContainSubstring(`"GracefulShutdown"`))
				Expect(out).To(ContainSubstring(`"GracefulRestart"`))
				Expect(out).To(ContainSubstring(`"ForceRestart"`))
			})

			It("should accept graceful shutdown action and VM is actually off", func() {
				_, err := runCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"GracefulShutdown"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIDeleted(ctx, k8sClient, ns)
			})

			It("should accept power on reset action and VM is actually running", func() {
				_, err := runCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"On"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIRunning(ctx, k8sClient, ns)
			})

			It("should accept graceful restart action and VM is stopped then started again", func() {
				_, err := runCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"GracefulRestart"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIPowerCycle(ctx, k8sClient, ns)
			})

			It("should accept force restart action and VM is stopped then started again", func() {
				_, err := runCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"ForceRestart"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIPowerCycle(ctx, k8sClient, ns)
			})

			It("should accept force off action and VM is actually off", func() {
				_, err := runCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"ForceOff"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIDeleted(ctx, k8sClient, ns)
			})

			It("should accept power on reset action and VM is actually running", func() {
				_, err := runCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"On"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIRunning(ctx, k8sClient, ns)
			})
		})

		Context("Boot configuration", func() {
			It("should return current boot configuration", func() {
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("GET", "/Systems/1", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring("Boot"))
			})

			It("should set boot to PXE once", func() {
				body := `{"Boot":{"BootSourceOverrideTarget":"Pxe","BootSourceOverrideEnabled":"Once"}}`
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("PATCH", "/Systems/1", body))
				Expect(err).NotTo(HaveOccurred())
				Expect(strings.TrimSpace(out)).To(SatisfyAny(
					ContainSubstring("200"),
					ContainSubstring("204"),
				))
				// PXE: interfaces first, then regular disks, then cdroms
				verifyVMBootOrder(ctx, k8sClient, ns,
					map[int]uint{0: 2, 1: 3}, // disks: regular=2, cdrom=3
					map[int]uint{0: 1},       // interface=1
				)
			})

			It("should set boot to PXE continuous", func() {
				body := `{"Boot":{"BootSourceOverrideTarget":"Pxe","BootSourceOverrideEnabled":"Continuous"}}`
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("PATCH", "/Systems/1", body))
				Expect(err).NotTo(HaveOccurred())
				Expect(strings.TrimSpace(out)).To(SatisfyAny(
					ContainSubstring("200"),
					ContainSubstring("204"),
				))
				// PXE: interfaces first, then regular disks, then cdroms
				verifyVMBootOrder(ctx, k8sClient, ns,
					map[int]uint{0: 2, 1: 3}, // disks: regular=2, cdrom=3
					map[int]uint{0: 1},       // interface=1
				)
			})

			It("should set boot to disk", func() {
				body := `{"Boot":{"BootSourceOverrideTarget":"Hdd","BootSourceOverrideEnabled":"Once"}}`
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("PATCH", "/Systems/1", body))
				Expect(err).NotTo(HaveOccurred())
				Expect(strings.TrimSpace(out)).To(SatisfyAny(
					ContainSubstring("200"),
					ContainSubstring("204"),
				))
				// HDD: regular disks first, then interfaces, then cdroms
				verifyVMBootOrder(ctx, k8sClient, ns,
					map[int]uint{0: 1, 1: 3}, // disks: regular=1, cdrom=3
					map[int]uint{0: 2},       // interface=2
				)
			})

			It("should set boot to Cd", func() {
				body := `{"Boot":{"BootSourceOverrideTarget":"Cd","BootSourceOverrideEnabled":"Once"}}`
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("PATCH", "/Systems/1", body))
				Expect(err).NotTo(HaveOccurred())
				Expect(strings.TrimSpace(out)).To(SatisfyAny(
					ContainSubstring("200"),
					ContainSubstring("204"),
				))
				// CD: cdroms first, then regular disks, then interfaces
				verifyVMBootOrder(ctx, k8sClient, ns,
					map[int]uint{0: 2, 1: 1}, // disks: regular=2, cdrom=1
					map[int]uint{0: 3},       // interface=3
				)
			})

			It("should set boot mode to UEFI", func() {
				body := `{"Boot":{"BootSourceOverrideMode":"UEFI"}}`
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("PATCH", "/Systems/1", body))
				Expect(err).NotTo(HaveOccurred())
				Expect(strings.TrimSpace(out)).To(SatisfyAny(
					ContainSubstring("200"),
					ContainSubstring("204"),
				))
			})

			It("should set boot mode to Legacy", func() {
				body := `{"Boot":{"BootSourceOverrideMode":"Legacy"}}`
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("PATCH", "/Systems/1", body))
				Expect(err).NotTo(HaveOccurred())
				Expect(strings.TrimSpace(out)).To(SatisfyAny(
					ContainSubstring("200"),
					ContainSubstring("204"),
				))
			})
		})

		Context("System and manager information", func() {
			It("should return system details", func() {
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("GET", "/Systems/1", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring("ComputerSystem"))
			})

			It("should return manager information", func() {
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("GET", "/Managers/BMC", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring("Manager"))
			})
		})
	})

	Context("Virtual Media operations", func() {
		BeforeAll(func() {
			if !util.VirtualMediaPrerequisitesMet() {
				Skip("Virtual media tests require CDI (datavolumes.cdi.kubevirt.io) and KubeVirt with DeclarativeHotplugVolumes feature gate enabled; see README.")
			}
		})

		Context("Virtual Media Redfish API", func() {
			It("should return virtual media resource CD1", func() {
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("GET", "/Managers/BMC/VirtualMedia/CD1", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring("VirtualMedia"))
				Expect(out).To(ContainSubstring("CD1"))
				Expect(out).To(SatisfyAny(
					ContainSubstring("Inserted"),
					ContainSubstring("Image"),
				))
			})

			It("should power off the VM", func() {
				_, err := runCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"ForceOff"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIDeleted(ctx, k8sClient, ns)
			})

			It("should accept InsertMedia action", func() {
				body := `{"Image":"https://releases.ubuntu.com/noble/ubuntu-24.04.3-live-server-amd64.iso","Inserted":true}`
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("POST", "/Managers/BMC/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia", body))
				Expect(err).NotTo(HaveOccurred())
				trimmed := strings.TrimSpace(out)
				Expect(trimmed).To(SatisfyAny(
					ContainSubstring("200"),
				))
			})

			It("should create a DataVolume after InsertMedia", func() {
				verifyDataVolumeExists(ctx, k8sClient, ns, agentVMName)
			})

			It("should update the VM spec volume list after InsertMedia", func() {
				verifyVMHasDataVolumeVolume(ctx, k8sClient, ns, agentVMName, agentVMName)
			})

			It("should return virtual media status after insert attempt", func() {
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("GET", "/Managers/BMC/VirtualMedia/CD1", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring("VirtualMedia"))
				Expect(out).To(ContainSubstring("CD1"))
			})

			It("should set boot to Cd and verify CDROM becomes boot index 1", func() {
				body := `{"Boot":{"BootSourceOverrideTarget":"Cd","BootSourceOverrideEnabled":"Once"}}`
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("PATCH", "/Systems/1", body))
				Expect(err).NotTo(HaveOccurred())
				Expect(strings.TrimSpace(out)).To(SatisfyAny(
					ContainSubstring("200"),
					ContainSubstring("204"),
				))
				// CD: cdroms first, then regular disks, then interfaces
				verifyVMBootOrder(ctx, k8sClient, ns,
					map[int]uint{0: 2, 1: 1}, // disks: containerdisk=2, cdrom=1
					map[int]uint{0: 3},       // interface=3
				)
			})

			It("should accept EjectMedia action", func() {
				out, err := runCurlRedfish(ctx, config, ns, redfishSession("POST", "/Managers/BMC/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia", `{}`))
				Expect(err).NotTo(HaveOccurred())
				Expect(strings.TrimSpace(out)).To(SatisfyAny(
					ContainSubstring("200"),
					ContainSubstring("204"),
				))
			})

			It("should remove the volume entry from VM spec after EjectMedia", func() {
				verifyVMHasNoDataVolumeVolume(ctx, k8sClient, ns, agentVMName)
			})

			It("should delete the DataVolume after EjectMedia", func() {
				verifyDataVolumeDeleted(ctx, k8sClient, ns, agentVMName)
			})
		})
	})

})
