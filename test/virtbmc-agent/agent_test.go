package virtbmcagent

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	"kubevirt.io/kubevirtbmc/pkg/util"
	testutil "kubevirt.io/kubevirtbmc/test/util"
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

		By("ensuring IPMI is disabled for a clean starting state")
		env.BMC.Spec.IPMI = nil
		Expect(k8sClient.Update(ctx, env.BMC)).To(Succeed())

		clientset, err := kubernetes.NewForConfig(config)
		Expect(err).NotTo(HaveOccurred())
		Expect(testutil.CreateIPMIToolPod(ctx, clientset, ns)).To(Succeed())
		Expect(testutil.CreateRedfishClientPod(ctx, clientset, ns)).To(Succeed())
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
			_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "status"))
			Expect(err).To(HaveOccurred(), "IPMI command should fail when IPMI is disabled")

			By("recording the current pod UID before enabling IPMI")
			podBefore, err := testutil.AgentPod(ctx, k8sClient, ns)
			Expect(err).NotTo(HaveOccurred())

			By("enabling IPMI on the VirtualMachineBMC")
			bmc := &bmcv1.VirtualMachineBMC{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: agentBMCName}, bmc)).To(Succeed())
			enabled := true
			orig := bmc.DeepCopy()
			bmc.Spec.IPMI = &bmcv1.IPMISpec{Enabled: &enabled}
			Expect(k8sClient.Patch(ctx, bmc, client.MergeFrom(orig))).To(Succeed())

			By("waiting for new agent pod to be recreated and ready")
			Eventually(testutil.PodRunningAndReadyWithNewUID(ctx, k8sClient, ns, podBefore.UID), agentTestTimeout, agentTestInterval).Should(BeTrue(), "new agent pod should become ready")

			By("creating a new Redfish session for the restarted pod")
			Eventually(func() error {
				authToken, err = testutil.CreateRedfishSession(ctx, config, ns, env.RedfishBaseURL, env.Username, env.Password)
				return err
			}, agentTestTimeout, agentTestInterval).Should(Succeed())
			Expect(authToken).NotTo(BeEmpty())
		})
	})

	Context("IPMI operations", func() {
		Context("Authentication", func() {
			It("should accept commands with correct username and password", func() {
				out, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "status"))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(SatisfyAny(
					ContainSubstring("Chassis Power is on"),
					ContainSubstring("Chassis Power is off"),
				))
			})

			It("should reject commands with wrong password", func() {
				wrongReq := ipmiReq("power", "status")
				wrongReq.Password = "wrongpass"
				_, stderr, err := testutil.RunIPMIInCluster(ctx, config, ns, wrongReq)
				Expect(err).To(HaveOccurred(), "IPMI command with wrong password should be rejected")
				Expect(stderr).To(ContainSubstring("Unable to establish IPMI v2 / RMCP+ session"))
			})

			It("should reject commands with wrong username", func() {
				wrongReq := ipmiReq("power", "status")
				wrongReq.Username = "baduser"
				_, stderr, err := testutil.RunIPMIInCluster(ctx, config, ns, wrongReq)
				Expect(err).To(HaveOccurred(), "IPMI command with wrong username should be rejected")
				Expect(stderr).To(ContainSubstring("Unable to establish IPMI v2 / RMCP+ session"))
			})

		})

		Context("Dual-stack (lan + lanplus)", func() {
			DescribeTable("should report power status",
				func(iface string) {
					req := ipmiReq("power", "status")
					req.Interface = iface
					out, _, err := testutil.RunIPMIInCluster(ctx, config, ns, req)
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
					_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, req)
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
					_, _, elapsed, err := testutil.RunIPMIInClusterTimed(ctx, config, ns, req)
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
				out, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "status"))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(SatisfyAny(
					ContainSubstring("Chassis Power is on"),
					ContainSubstring("Chassis Power is off"),
				))
			})

			It("should accept power off and VM is actually off", func() {
				_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "off"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIDeleted(ctx, k8sClient, ns)
			})

			It("should accept power on and VM is actually running", func() {
				_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "on"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIRunning(ctx, k8sClient, ns)
			})

			It("should accept power cycle and VM is stopped then started again", func() {
				_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "cycle"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIPowerCycle(ctx, k8sClient, ns)
			})

			It("should accept power soft (graceful ACPI shutdown) and VM is actually off", func() {
				_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "soft"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIDeleted(ctx, k8sClient, ns)
			})

			It("should accept power on and VM is actually running", func() {
				_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "on"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIRunning(ctx, k8sClient, ns)
			})

			It("should accept power reset (hard reset) and VM is stopped then started again", func() {
				_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "reset"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIPowerCycle(ctx, k8sClient, ns)
			})

			It("should accept power reset while VMI exists but VM is not Ready yet", func() {
				// Reproduce soft→on→reset race: after power-on, a VMI appears
				// before VM.Status.Ready flips. Reset in that window must
				// Restart, not fall back to PowerOn (Always would swallow it).
				waitForVMIRunning(ctx, k8sClient, ns)
				_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "soft"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIDeleted(ctx, k8sClient, ns)

				_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "on"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIPresentBeforeReady(ctx, k8sClient, ns)

				_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "reset"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIPowerCycle(ctx, k8sClient, ns)
			})

			It("should return retryable error when power-on races with VMI cleanup", func() {
				waitForVMIRunning(ctx, k8sClient, ns)
				_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "soft"))
				Expect(err).NotTo(HaveOccurred())

				// Power on again before VMI cleanup completes: while the VMI
				// lingers the handler returns Node Busy (0xC0), and a retry
				// succeeds once the VMI is gone.
				var sawNodeBusy bool
				Eventually(func() error {
					_, stderr, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "on"))
					if err != nil {
						sawNodeBusy = true
						Expect(stderr).To(ContainSubstring("Node busy"),
							"expected Node Busy retryable error, got stderr=%q", stderr)
					}
					return err
				}, vmPowerStatusTimeout, agentTestInterval).Should(Succeed(),
					"power on should eventually succeed after VMI cleanup")
				Expect(sawNodeBusy).To(BeTrue(),
					"expected at least one Node Busy retryable error before success")

				waitForVMIRunning(ctx, k8sClient, ns)
			})

			It("should treat repeated power-on as idempotent during VM startup", func() {
				waitForVMIRunning(ctx, k8sClient, ns)
				_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "soft"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIDeleted(ctx, k8sClient, ns)

				// The second power-on may arrive before the VMI is Ready;
				// with RunStrategyAlways idempotency it must succeed, not
				// return Node Busy.
				_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "on"))
				Expect(err).NotTo(HaveOccurred())
				_, stderr, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "on"))
				Expect(err).NotTo(HaveOccurred(),
					"repeated power-on should succeed; stderr=%q", stderr)

				waitForVMIRunning(ctx, k8sClient, ns)
			})

			It("should treat repeated power-on as idempotent for Manual runStrategy", func() {
				waitForVMIRunning(ctx, k8sClient, ns)
				_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "soft"))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIDeleted(ctx, k8sClient, ns)

				// Manual VMs queue Start via StateChangeRequests; a second
				// immediate power-on should succeed idempotently.
				setVMRunStrategy(ctx, k8sClient, ns, kubevirtv1.RunStrategyManual)
				defer setVMRunStrategy(ctx, k8sClient, ns, kubevirtv1.RunStrategyAlways)

				_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "on"))
				Expect(err).NotTo(HaveOccurred())
				_, stderr, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "on"))
				Expect(err).NotTo(HaveOccurred(),
					"repeated Manual power-on should succeed; stderr=%q", stderr)

				waitForVMIRunning(ctx, k8sClient, ns)
			})

			It("should treat repeated power-off as idempotent", func() {
				waitForVMIRunning(ctx, k8sClient, ns)
				_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "off"))
				Expect(err).NotTo(HaveOccurred())

				// Second power-off while VMI is being torn down should
				// succeed idempotently.
				_, stderr, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "off"))
				Expect(err).NotTo(HaveOccurred(),
					"repeated power-off should succeed; stderr=%q", stderr)

				waitForVMIDeleted(ctx, k8sClient, ns)
			})

			It("should accept power soft immediately after power on", func() {
				// KubeVirt allows Stop to interrupt a pending/starting VMI, so this
				// should succeed (unlike soft→on, which returns Node busy).
				waitForVMIDeleted(ctx, k8sClient, ns)
				_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "on"))
				Expect(err).NotTo(HaveOccurred())

				_, stderr, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "soft"))
				Expect(err).NotTo(HaveOccurred(),
					"power soft after power on should succeed; stderr=%q", stderr)

				waitForVMIDeleted(ctx, k8sClient, ns)
			})
		})

		Context("Boot device configuration", func() {
			BeforeEach(func() {
				resetBootState(ctx, k8sClient, ns)
			})

			It("should set boot device to PXE", func() {
				out, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "pxe"))
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
				out, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "disk"))
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
				out, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "cdrom"))
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

			Context("oneshot boot order", func() {
				It("should save boot override status on oneshot PXE", func() {
					_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "pxe"))
					Expect(err).NotTo(HaveOccurred())
					// PXE: interfaces first, then regular disks, then cdroms
					verifyVMBootOrder(ctx, k8sClient, ns,
						map[int]uint{0: 2, 1: 3},
						map[int]uint{0: 1},
					)
					verifyBMCBootOverride(ctx, k8sClient, ns, true)
					verifyBMCBootOverrideMode(ctx, k8sClient, ns, bmcv1.BootOverrideModeOneshot)
				})

				It("should save persistent override marker on persistent PXE", func() {
					_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "pxe", "options=persistent"))
					Expect(err).NotTo(HaveOccurred())
					verifyVMBootOrder(ctx, k8sClient, ns,
						map[int]uint{0: 2, 1: 3},
						map[int]uint{0: 1},
					)
					verifyBMCBootOverride(ctx, k8sClient, ns, true)
					verifyBMCBootOverrideMode(ctx, k8sClient, ns, bmcv1.BootOverrideModePersistent)
				})

				It("should cancel oneshot override with bootdev none", func() {
					By("setting oneshot PXE first")
					_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "pxe"))
					Expect(err).NotTo(HaveOccurred())
					verifyVMBootOrder(ctx, k8sClient, ns,
						map[int]uint{0: 2, 1: 3},
						map[int]uint{0: 1},
					)
					verifyBMCBootOverride(ctx, k8sClient, ns, true)

					By("cancelling with bootdev none")
					_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "none"))
					Expect(err).NotTo(HaveOccurred())

					By("verifying original boot order restored and backup removed")
					verifyVMBootOrderNot(ctx, k8sClient, ns, map[int]uint{0: 1, 1: 3}, map[int]uint{0: 2})
					verifyBMCBootOverride(ctx, k8sClient, ns, false)
				})

				It("should restore only devices saved in oneshot backup and leave added devices unchanged", func() {
					DeferCleanup(func() {
						removeVMDiskAndVolumeIfExists(ctx, k8sClient, ns, issue191DiskName)
						removeVMDiskAndVolumeIfExists(ctx, k8sClient, ns, issue191RemovedDisk)
					})

					By("setting original boot orders that will be saved in the oneshot backup")
					setVMDiskBootOrder(ctx, k8sClient, ns, 0, 9)
					addEmptyDiskWithBootOrder(ctx, k8sClient, ns, issue191RemovedDisk, 7)

					By("setting oneshot PXE")
					_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "pxe"))
					Expect(err).NotTo(HaveOccurred())
					verifyVMNamedBootOrders(ctx, k8sClient, ns,
						map[string]*uint{
							"containerdisk":     util.Ptr[uint](2),
							"cdrom":             util.Ptr[uint](4),
							issue191RemovedDisk: util.Ptr[uint](3),
						},
						map[string]*uint{"default": util.Ptr[uint](1)},
					)
					verifyBMCBootOverride(ctx, k8sClient, ns, true)

					By("removing a device that existed when the backup was saved")
					removeVMDiskAndVolumeIfExists(ctx, k8sClient, ns, issue191RemovedDisk)

					By("adding a new device with its own boot order after the backup was saved")
					addEmptyDiskWithBootOrder(ctx, k8sClient, ns, issue191DiskName, 8)
					verifyVMNamedBootOrders(ctx, k8sClient, ns,
						map[string]*uint{
							"containerdisk":  util.Ptr[uint](2),
							"cdrom":          util.Ptr[uint](4),
							issue191DiskName: util.Ptr[uint](8),
						},
						map[string]*uint{"default": util.Ptr[uint](1)},
					)

					By("powering off the VM")
					_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "off"))
					Expect(err).NotTo(HaveOccurred())
					waitForVMIDeleted(ctx, k8sClient, ns)

					By("powering on the VM to consume the oneshot backup")
					_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "on"))
					Expect(err).NotTo(HaveOccurred())
					waitForVMIRunning(ctx, k8sClient, ns)

					By("verifying existing devices are restored, removed devices are skipped, and added devices are left unchanged")
					verifyVMNamedBootOrders(ctx, k8sClient, ns,
						map[string]*uint{
							"containerdisk":  util.Ptr[uint](9),
							"cdrom":          nil,
							issue191DiskName: util.Ptr[uint](8),
						},
						map[string]*uint{"default": nil},
					)
					verifyBMCBootOverride(ctx, k8sClient, ns, false)
				})

				It("should set EFI firmware template on running VM", func() {
					_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "pxe", "options=efiboot"))
					Expect(err).NotTo(HaveOccurred())
					verifyVMBootOrder(ctx, k8sClient, ns,
						map[int]uint{0: 2, 1: 3},
						map[int]uint{0: 1},
					)
					verifyVMFirmware(ctx, k8sClient, ns, true)
					verifyBMCBootOverride(ctx, k8sClient, ns, true)
					verifyBMCBootOverrideMode(ctx, k8sClient, ns, bmcv1.BootOverrideModeOneshot)
				})

				It("should set EFI firmware on stopped VM", func() {
					By("stopping the VM")
					_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "off"))
					Expect(err).NotTo(HaveOccurred())
					waitForVMIDeleted(ctx, k8sClient, ns)

					By("setting EFI firmware")
					_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "disk", "options=persistent,efiboot"))
					Expect(err).NotTo(HaveOccurred())
					verifyVMBootOrder(ctx, k8sClient, ns,
						map[int]uint{0: 1, 1: 3},
						map[int]uint{0: 2},
					)
					verifyVMFirmware(ctx, k8sClient, ns, true)
					// options=persistent,efiboot must not be downgraded to oneshot
					verifyBMCBootOverrideMode(ctx, k8sClient, ns, bmcv1.BootOverrideModePersistent)

					By("restarting the VM")
					_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "on"))
					Expect(err).NotTo(HaveOccurred())
					waitForVMIRunning(ctx, k8sClient, ns)
				})

				It("should restore original boot order after multiple oneshot overrides before reboot", func() {
					By("setting first oneshot to PXE")
					_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "pxe"))
					Expect(err).NotTo(HaveOccurred())
					verifyVMBootOrder(ctx, k8sClient, ns,
						map[int]uint{0: 2, 1: 3},
						map[int]uint{0: 1},
					)
					verifyBMCBootOverride(ctx, k8sClient, ns, true)

					By("setting second oneshot to cdrom before reboot")
					_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "cdrom"))
					Expect(err).NotTo(HaveOccurred())
					verifyVMBootOrder(ctx, k8sClient, ns,
						map[int]uint{0: 2, 1: 1},
						map[int]uint{0: 3},
					)
					verifyBMCBootOverride(ctx, k8sClient, ns, true)

					By("powering off the VM")
					_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "off"))
					Expect(err).NotTo(HaveOccurred())
					waitForVMIDeleted(ctx, k8sClient, ns)

					By("powering on the VM to consume the oneshot")
					_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("power", "on"))
					Expect(err).NotTo(HaveOccurred())
					waitForVMIRunning(ctx, k8sClient, ns)

					By("verifying the original boot order is restored and backup is cleared")
					verifyVMNamedBootOrders(ctx, k8sClient, ns,
						map[string]*uint{"containerdisk": nil, "cdrom": nil},
						map[string]*uint{"default": nil},
					)
					verifyBMCBootOverride(ctx, k8sClient, ns, false)

					By("power cycling to get a clean VMI from the current template")
					_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "power", "cycle"))
					Expect(err).NotTo(HaveOccurred())
					waitForVMIPowerCycle(ctx, k8sClient, ns)

					By("waiting for guest agent to be connected")
					waitForGuestAgent(ctx, k8sClient, ns)

					By("setting oneshot HDD")
					_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "disk"))
					Expect(err).NotTo(HaveOccurred())
					verifyVMBootOrder(ctx, k8sClient, ns,
						map[int]uint{0: 1, 1: 3},
						map[int]uint{0: 2},
					)
					verifyBMCBootOverride(ctx, k8sClient, ns, true)

					By("triggering guest OS reboot — rebootPolicy=Terminate destroys and recreates the VMI")
					vmiBefore := &kubevirtv1.VirtualMachineInstance{}
					Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: agentVMName}, vmiBefore)).To(Succeed())
					uidBefore := vmiBefore.UID
					triggerGuestReboot(ctx, config, k8sClient, ns)
					waitForVMIPowerCycle(ctx, k8sClient, ns)
					vmiAfter := &kubevirtv1.VirtualMachineInstance{}
					Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: agentVMName}, vmiAfter)).To(Succeed())
					Expect(vmiAfter.UID).NotTo(Equal(uidBefore), "Terminate should recreate the VMI with a new UID")

					By("verifying oneshot was consumed: boot order restored, backup cleared")
					verifyBMCBootOverride(ctx, k8sClient, ns, false)
					verifyVMBootOrderNot(ctx, k8sClient, ns, map[int]uint{0: 1, 1: 3}, map[int]uint{0: 2})
				})

				It("should consume oneshot after guest OS reboot on a VM with rebootPolicy=Terminate", Label("Slow"), func() {
					By("power cycling to get a clean VMI from the current template")
					_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "power", "cycle"))
					Expect(err).NotTo(HaveOccurred())
					waitForVMIPowerCycle(ctx, k8sClient, ns)

					By("waiting for guest agent to be connected")
					waitForGuestAgent(ctx, k8sClient, ns)

					By("setting oneshot HDD")
					_, _, err = testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "disk"))
					Expect(err).NotTo(HaveOccurred())
					verifyVMBootOrder(ctx, k8sClient, ns,
						map[int]uint{0: 1, 1: 3},
						map[int]uint{0: 2},
					)
					verifyBMCBootOverride(ctx, k8sClient, ns, true)

					By("triggering guest OS reboot — rebootPolicy=Terminate destroys and recreates the VMI")
					vmiBefore := &kubevirtv1.VirtualMachineInstance{}
					Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: agentVMName}, vmiBefore)).To(Succeed())
					uidBefore := vmiBefore.UID
					triggerGuestReboot(ctx, config, k8sClient, ns)
					waitForVMIPowerCycle(ctx, k8sClient, ns)
					vmiAfter := &kubevirtv1.VirtualMachineInstance{}
					Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: agentVMName}, vmiAfter)).To(Succeed())
					Expect(vmiAfter.UID).NotTo(Equal(uidBefore), "Terminate should recreate the VMI with a new UID")

					By("verifying oneshot was consumed: boot order restored, backup cleared")
					verifyBMCBootOverride(ctx, k8sClient, ns, false)
					verifyVMBootOrderNot(ctx, k8sClient, ns, map[int]uint{0: 1, 1: 3}, map[int]uint{0: 2})
				})
			})

			Context("bootparam get 5", func() {
				It("should read back oneshot PXE boot flags", func() {
					By("setting oneshot PXE")
					_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "pxe"))
					Expect(err).NotTo(HaveOccurred())

					By("reading back boot flags")
					out, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootparam", "get", "5"))
					Expect(err).NotTo(HaveOccurred())
					Expect(out).To(And(
						ContainSubstring("Boot Flag Valid"),
						ContainSubstring("only next boot"),
						ContainSubstring("Force PXE"),
					))
				})

				It("should read back persistent HDD boot flags", func() {
					By("setting persistent disk")
					_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "disk", "options=persistent"))
					Expect(err).NotTo(HaveOccurred())

					By("reading back boot flags")
					out, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootparam", "get", "5"))
					Expect(err).NotTo(HaveOccurred())
					Expect(out).To(And(
						ContainSubstring("Boot Flag Valid"),
						ContainSubstring("all future boots"),
						ContainSubstring("Force Boot from default Hard-Drive"),
					))
				})

				It("should read back EFI oneshot CD boot flags", func() {
					By("setting EFI oneshot CD")
					_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "cdrom", "options=efiboot"))
					Expect(err).NotTo(HaveOccurred())

					By("reading back boot flags")
					out, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootparam", "get", "5"))
					Expect(err).NotTo(HaveOccurred())
					Expect(out).To(And(
						ContainSubstring("Boot Flag Valid"),
						ContainSubstring("only next boot"),
						ContainSubstring("Force Boot from CD/DVD"),
						ContainSubstring("BIOS EFI boot"),
					))
				})

				It("should read back persistent EFI PXE boot flags", func() {
					By("setting persistent EFI PXE")
					_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "pxe", "options=persistent,efiboot"))
					Expect(err).NotTo(HaveOccurred())

					By("reading back boot flags")
					out, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootparam", "get", "5"))
					Expect(err).NotTo(HaveOccurred())
					Expect(out).To(And(
						ContainSubstring("Boot Flag Valid"),
						ContainSubstring("all future boots"),
						ContainSubstring("Force PXE"),
						ContainSubstring("BIOS EFI boot"),
					))
				})

				It("should survive agent pod restart and persist boot flags", func() {
					By("setting persistent PXE as a known state")
					_, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootdev", "pxe", "options=persistent"))
					Expect(err).NotTo(HaveOccurred())

					By("recording the current agent pod UID")
					podBefore, err := testutil.AgentPod(ctx, k8sClient, ns)
					Expect(err).NotTo(HaveOccurred())

					By("deleting the agent pod to trigger restart")
					podToDelete, err := testutil.AgentPod(ctx, k8sClient, ns)
					Expect(err).NotTo(HaveOccurred())
					Expect(k8sClient.Delete(ctx, podToDelete)).To(Succeed())

					By("waiting for new agent pod to be recreated and ready")
					Eventually(testutil.PodRunningAndReadyWithNewUID(ctx, k8sClient, ns, podBefore.UID), agentTestTimeout, agentTestInterval).Should(BeTrue(), "new agent pod should become ready")

					By("verifying bootparam get 5 returns correct state from CR status after restart")
					out, _, err := testutil.RunIPMIInCluster(ctx, config, ns, ipmiReq("chassis", "bootparam", "get", "5"))
					Expect(err).NotTo(HaveOccurred())
					Expect(out).To(And(
						ContainSubstring("Boot Flag Valid"),
						ContainSubstring("all future boots"),
						ContainSubstring("Force PXE"),
					))

					By("re-creating Redfish session for restarted pod")
					Eventually(func() error {
						authToken, err = testutil.CreateRedfishSession(ctx, config, ns, env.RedfishBaseURL, env.Username, env.Password)
						return err
					}, agentTestTimeout, agentTestInterval).Should(Succeed())
					Expect(authToken).NotTo(BeEmpty())

					By("verifying Redfish GET also returns correct boot state after restart")
					out2, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("GET", "/Systems/1", ""))
					Expect(err).NotTo(HaveOccurred())
					Expect(out2).To(And(
						ContainSubstring(`"BootSourceOverrideTarget":"Pxe"`),
						ContainSubstring(`"BootSourceOverrideEnabled":"Continuous"`),
					))
				})
			})
		})
	})

	Context("Redfish operations", func() {
		Context("Authentication", func() {
			It("should allow access with basic auth", func() {
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishBasic("GET", "/Systems/1", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring(`"@odata.id":"/redfish/v1/Systems/1"`))
			})

			It("should reject access with wrong password", func() {
				out, err := testutil.RunCurlRedfish(ctx, config, ns, RedfishRequest{
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
				out, err := testutil.RunCurlRedfish(ctx, config, ns, RedfishRequest{
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
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("GET", "", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring(`"@odata.id":"/redfish/v1"`))
				Expect(out).To(ContainSubstring("RedfishVersion"))
				Expect(out).To(ContainSubstring("Systems"))
				Expect(out).To(ContainSubstring("Managers"))
			})
		})

		Context("Power management", func() {
			It("should return system power state", func() {
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("GET", "/Systems/1", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(SatisfyAny(
					ContainSubstring("On"),
					ContainSubstring("Off"),
				))
			})

			It("should advertise supported reset action values", func() {
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("GET", "/Systems/1", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring(`"ResetType@Redfish.AllowableValues"`))
				Expect(out).To(ContainSubstring(`"On"`))
				Expect(out).To(ContainSubstring(`"ForceOff"`))
				Expect(out).To(ContainSubstring(`"GracefulShutdown"`))
				Expect(out).To(ContainSubstring(`"GracefulRestart"`))
				Expect(out).To(ContainSubstring(`"ForceRestart"`))
			})

			It("should accept graceful shutdown action and VM is actually off", func() {
				_, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"GracefulShutdown"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIDeleted(ctx, k8sClient, ns)
			})

			It("should accept power on reset action and VM is actually running", func() {
				_, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"On"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIRunning(ctx, k8sClient, ns)
			})

			It("should accept graceful restart action and VM is stopped then started again", func() {
				_, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"GracefulRestart"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIPowerCycle(ctx, k8sClient, ns)
			})

			It("should accept force restart action and VM is stopped then started again", func() {
				_, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"ForceRestart"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIPowerCycle(ctx, k8sClient, ns)
			})

			It("should accept force off action and VM is actually off", func() {
				_, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"ForceOff"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIDeleted(ctx, k8sClient, ns)
			})

			It("should accept power on reset action and VM is actually running", func() {
				_, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"On"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIRunning(ctx, k8sClient, ns)
			})
		})

		Context("Boot configuration", func() {
			BeforeEach(func() {
				resetBootState(ctx, k8sClient, ns)
			})

			It("should return current boot configuration", func() {
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("GET", "/Systems/1", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring("Boot"))
			})

			It("should set boot to PXE once", func() {
				By("setting a one-time PXE boot override through Redfish")
				body := `{"Boot":{"BootSourceOverrideTarget":"Pxe","BootSourceOverrideEnabled":"Once"}}`
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("PATCH", "/Systems/1", body))
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
				verifyBMCBootOverride(ctx, k8sClient, ns, true)

				By("powering off the VM")
				_, err = testutil.RunCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"ForceOff"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIDeleted(ctx, k8sClient, ns)

				By("powering on the VM to consume the one-time override")
				_, err = testutil.RunCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"On"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIRunning(ctx, k8sClient, ns)

				By("verifying the original boot order is restored")
				verifyVMBootOrderNot(ctx, k8sClient, ns, map[int]uint{0: 2, 1: 3}, map[int]uint{0: 1})
				verifyBMCBootOverride(ctx, k8sClient, ns, false)
			})

			It("should set boot to PXE continuous", func() {
				verifyBMCBootOverride(ctx, k8sClient, ns, false)
				body := `{"Boot":{"BootSourceOverrideTarget":"Pxe","BootSourceOverrideEnabled":"Continuous"}}`
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("PATCH", "/Systems/1", body))
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
				verifyBMCBootOverride(ctx, k8sClient, ns, true)
			})

			It("should set boot to disk", func() {
				body := `{"Boot":{"BootSourceOverrideTarget":"Hdd","BootSourceOverrideEnabled":"Once"}}`
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("PATCH", "/Systems/1", body))
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
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("PATCH", "/Systems/1", body))
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
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("PATCH", "/Systems/1", body))
				Expect(err).NotTo(HaveOccurred())
				Expect(strings.TrimSpace(out)).To(SatisfyAny(
					ContainSubstring("200"),
					ContainSubstring("204"),
				))
				verifyVMFirmware(ctx, k8sClient, ns, true)
			})

			It("should set boot mode to Legacy", func() {
				body := `{"Boot":{"BootSourceOverrideMode":"Legacy"}}`
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("PATCH", "/Systems/1", body))
				Expect(err).NotTo(HaveOccurred())
				Expect(strings.TrimSpace(out)).To(SatisfyAny(
					ContainSubstring("200"),
					ContainSubstring("204"),
				))
				verifyVMFirmware(ctx, k8sClient, ns, false)
			})
		})

		Context("System and manager information", func() {
			It("should return system details including boot override state", func() {
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("GET", "/Systems/1", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring("ComputerSystem"))
				Expect(out).To(ContainSubstring(`"BootSourceOverrideEnabled"`))
				Expect(out).To(ContainSubstring(`"BootSourceOverrideTarget"`))
				Expect(out).To(ContainSubstring(`"BootSourceOverrideMode"`))
			})

			It("should return manager information", func() {
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("GET", "/Managers/BMC", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring("Manager"))
			})
		})
	})

	Context("Virtual Media operations", func() {
		BeforeAll(func() {
			if !testutil.VirtualMediaPrerequisitesMet() {
				Skip("Virtual media tests require CDI (datavolumes.cdi.kubevirt.io) and KubeVirt with DeclarativeHotplugVolumes feature gate enabled; see README.")
			}
		})

		Context("Virtual Media Redfish API", func() {
			It("should return virtual media resource CD1", func() {
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("GET", "/Managers/BMC/VirtualMedia/CD1", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring("VirtualMedia"))
				Expect(out).To(ContainSubstring("CD1"))
				Expect(out).To(SatisfyAny(
					ContainSubstring("Inserted"),
					ContainSubstring("Image"),
				))
			})

			It("should power off the VM", func() {
				_, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("POST", "/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"ForceOff"}`))
				Expect(err).NotTo(HaveOccurred())
				waitForVMIDeleted(ctx, k8sClient, ns)
			})

			It("should accept InsertMedia action", func() {
				body := `{"Image":"https://releases.ubuntu.com/noble/ubuntu-24.04.3-live-server-amd64.iso","Inserted":true}`
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("POST", "/Managers/BMC/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia", body))
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
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("GET", "/Managers/BMC/VirtualMedia/CD1", ""))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring("VirtualMedia"))
				Expect(out).To(ContainSubstring("CD1"))
			})

			It("should set boot to Cd and verify CDROM becomes boot index 1", func() {
				body := `{"Boot":{"BootSourceOverrideTarget":"Cd","BootSourceOverrideEnabled":"Once"}}`
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("PATCH", "/Systems/1", body))
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
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("POST", "/Managers/BMC/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia", `{}`))
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

		Context("Virtual Media storageClassName override", func() {
			const wantClass = "kubevirtbmc-e2e-override-sc"

			BeforeAll(func() {
				By("creating a dedicated StorageClass")
				Expect(k8sClient.Create(ctx, newStorageClass(wantClass))).To(Succeed())
				DeferCleanup(func() {
					_ = k8sClient.Delete(ctx, newStorageClass(wantClass))
				})

				By("setting storageClassName on the VirtualMachineBMC")
				bmc := &bmcv1.VirtualMachineBMC{}
				Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: agentBMCName}, bmc)).To(Succeed())
				orig := bmc.DeepCopy()
				bmc.Spec.StorageClassName = util.Ptr(wantClass)
				Expect(k8sClient.Patch(ctx, bmc, client.MergeFrom(orig))).To(Succeed())
			})

			It("should insert media and create a DataVolume using the configured StorageClass", func() {
				body := `{"Image":"https://releases.ubuntu.com/noble/ubuntu-24.04.3-live-server-amd64.iso","Inserted":true}`
				out, err := testutil.RunCurlRedfish(ctx, config, ns, redfishSession("POST", "/Managers/BMC/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia", body))
				Expect(err).NotTo(HaveOccurred())
				Expect(strings.TrimSpace(out)).To(ContainSubstring("200"))

				verifyDataVolumeExists(ctx, k8sClient, ns, agentVMName)
				verifyDataVolumeStorageClass(ctx, k8sClient, ns, agentVMName, wantClass)
			})
		})
	})

})
