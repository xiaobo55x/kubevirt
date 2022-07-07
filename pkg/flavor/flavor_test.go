package flavor_test

import (
	"context"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/testing"
	"k8s.io/utils/pointer"

	k8sfield "k8s.io/apimachinery/pkg/util/validation/field"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kubevirt.io/client-go/api"
	"kubevirt.io/client-go/kubecli"

	"kubevirt.io/kubevirt/pkg/flavor"

	v1 "kubevirt.io/api/core/v1"
	apiflavor "kubevirt.io/api/flavor"
	flavorv1alpha1 "kubevirt.io/api/flavor/v1alpha1"
	fakeclientset "kubevirt.io/client-go/generated/kubevirt/clientset/versioned/fake"
	"kubevirt.io/client-go/generated/kubevirt/clientset/versioned/typed/flavor/v1alpha1"
)

const resourceUID types.UID = "9160e5de-2540-476a-86d9-af0081aee68a"
const resourceGeneration int64 = 1

var _ = Describe("Flavor and Preferences", func() {
	var (
		ctrl              *gomock.Controller
		flavorMethods     flavor.Methods
		vm                *v1.VirtualMachine
		vmi               *v1.VirtualMachineInstance
		virtClient        *kubecli.MockKubevirtClient
		vmInterface       *kubecli.MockVirtualMachineInterface
		fakeFlavorClients v1alpha1.FlavorV1alpha1Interface
		k8sClient         *k8sfake.Clientset
	)

	expectControllerRevisionCreation := func(flavorSpecRevision *appsv1.ControllerRevision) {
		k8sClient.Fake.PrependReactor("create", "controllerrevisions", func(action testing.Action) (handled bool, obj runtime.Object, err error) {
			created, ok := action.(testing.CreateAction)
			Expect(ok).To(BeTrue())

			createObj := created.GetObject().(*appsv1.ControllerRevision)
			Expect(createObj).To(Equal(flavorSpecRevision))

			return true, created.GetObject(), nil
		})
	}

	BeforeEach(func() {

		k8sClient = k8sfake.NewSimpleClientset()
		ctrl = gomock.NewController(GinkgoT())
		virtClient = kubecli.NewMockKubevirtClient(ctrl)
		vmInterface = kubecli.NewMockVirtualMachineInterface(ctrl)
		virtClient.EXPECT().VirtualMachine(metav1.NamespaceDefault).Return(vmInterface).AnyTimes()
		virtClient.EXPECT().AppsV1().Return(k8sClient.AppsV1()).AnyTimes()
		fakeFlavorClients = fakeclientset.NewSimpleClientset().FlavorV1alpha1()

		flavorMethods = flavor.NewMethods(virtClient)

		vm = kubecli.NewMinimalVM("testvm")
		vm.Namespace = k8sv1.NamespaceDefault

	})

	Context("Find and store Flavor Spec", func() {

		It("find returns nil when no flavor is specified", func() {
			vm.Spec.Flavor = nil
			spec, err := flavorMethods.FindFlavorSpec(vm)
			Expect(err).ToNot(HaveOccurred())
			Expect(spec).To(BeNil())
		})

		It("find returns error when invalid Flavor Kind is specified", func() {
			vm.Spec.Flavor = &v1.FlavorMatcher{
				Name: "foo",
				Kind: "bar",
			}
			spec, err := flavorMethods.FindFlavorSpec(vm)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("got unexpected kind in FlavorMatcher"))
			Expect(spec).To(BeNil())
		})

		It("store returns error when flavorMatcher kind is invalid", func() {
			vm.Spec.Flavor = &v1.FlavorMatcher{
				Kind: "foobar",
			}
			Expect(flavorMethods.StoreControllerRevisions(vm)).To(MatchError(ContainSubstring("got unexpected kind in FlavorMatcher")))
		})

		It("store returns nil when no flavorMatcher is specified", func() {
			vm.Spec.Flavor = nil
			Expect(flavorMethods.StoreControllerRevisions(vm)).To(Succeed())
		})

		Context("Using global ClusterFlavor", func() {
			var clusterFlavor *flavorv1alpha1.VirtualMachineClusterFlavor
			var fakeClusterFlavorClient v1alpha1.VirtualMachineClusterFlavorInterface

			BeforeEach(func() {

				fakeClusterFlavorClient = fakeFlavorClients.VirtualMachineClusterFlavors()
				virtClient.EXPECT().VirtualMachineClusterFlavor().Return(fakeClusterFlavorClient).AnyTimes()

				clusterFlavor = &flavorv1alpha1.VirtualMachineClusterFlavor{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "test-cluster-flavor",
						UID:        resourceUID,
						Generation: resourceGeneration,
					},
					Spec: flavorv1alpha1.VirtualMachineFlavorSpec{
						CPU: flavorv1alpha1.CPUFlavor{
							Guest: uint32(2),
						},
					},
				}

				_, err := virtClient.VirtualMachineClusterFlavor().Create(context.Background(), clusterFlavor, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				vm.Spec.Flavor = &v1.FlavorMatcher{
					Name: clusterFlavor.Name,
					Kind: apiflavor.ClusterSingularResourceName,
				}
			})

			It("returns expected flavor", func() {

				f, err := flavorMethods.FindFlavorSpec(vm)
				Expect(err).ToNot(HaveOccurred())
				Expect(*f).To(Equal(clusterFlavor.Spec))
			})

			It("find fails when flavor does not exist", func() {
				vm.Spec.Flavor.Name = "non-existing-flavor"
				_, err := flavorMethods.FindFlavorSpec(vm)
				Expect(err).To(HaveOccurred())
				Expect(errors.IsNotFound(err)).To(BeTrue())
			})

			It("store VirtualMachineClusterFlavor ControllerRevision", func() {

				clusterFlavorControllerRevision, err := flavor.CreateFlavorControllerRevision(vm, flavor.GetRevisionName(vm.Name, clusterFlavor.Name, clusterFlavor.UID, clusterFlavor.Generation), clusterFlavor.TypeMeta.APIVersion, &clusterFlavor.Spec)
				Expect(err).ToNot(HaveOccurred())

				expectedRevisionNamePatch, err := flavor.GenerateRevisionNamePatch(clusterFlavorControllerRevision, nil)
				Expect(err).ToNot(HaveOccurred())

				vmInterface.EXPECT().Patch(vm.Name, types.JSONPatchType, expectedRevisionNamePatch, &metav1.PatchOptions{})

				expectControllerRevisionCreation(clusterFlavorControllerRevision)

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(Succeed())
				Expect(vm.Spec.Flavor.RevisionName).To(Equal(clusterFlavorControllerRevision.Name))

			})

			It("store returns a nil revision when RevisionName already populated", func() {
				clusterFlavorControllerRevision, err := flavor.CreateFlavorControllerRevision(vm, flavor.GetRevisionName(vm.Name, clusterFlavor.Name, clusterFlavor.UID, clusterFlavor.Generation), clusterFlavor.TypeMeta.APIVersion, &clusterFlavor.Spec)
				Expect(err).ToNot(HaveOccurred())

				_, err = virtClient.AppsV1().ControllerRevisions(vm.Namespace).Create(context.Background(), clusterFlavorControllerRevision, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				vm.Spec.Flavor = &v1.FlavorMatcher{
					Name:         clusterFlavor.Name,
					RevisionName: clusterFlavorControllerRevision.Name,
					Kind:         apiflavor.ClusterSingularResourceName,
				}

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(Succeed())
				Expect(vm.Spec.Flavor.RevisionName).To(Equal(clusterFlavorControllerRevision.Name))
			})

			It("store fails when flavor does not exist", func() {
				vm.Spec.Flavor.Name = "non-existing-flavor"

				err := flavorMethods.StoreControllerRevisions(vm)
				Expect(err).To(HaveOccurred())
				Expect(errors.IsNotFound(err)).To(BeTrue())
			})

			It("store ControllerRevision succeeds if a revision exists with expected data", func() {

				flavorControllerRevision, err := flavor.CreateFlavorControllerRevision(vm, flavor.GetRevisionName(vm.Name, clusterFlavor.Name, clusterFlavor.UID, clusterFlavor.Generation), clusterFlavor.TypeMeta.APIVersion, &clusterFlavor.Spec)
				Expect(err).ToNot(HaveOccurred())

				_, err = virtClient.AppsV1().ControllerRevisions(vm.Namespace).Create(context.Background(), flavorControllerRevision, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				expectedRevisionNamePatch, err := flavor.GenerateRevisionNamePatch(flavorControllerRevision, nil)
				Expect(err).ToNot(HaveOccurred())

				vmInterface.EXPECT().Patch(vm.Name, types.JSONPatchType, expectedRevisionNamePatch, &metav1.PatchOptions{})

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(Succeed())
				Expect(vm.Spec.Flavor.RevisionName).To(Equal(flavorControllerRevision.Name))

			})

			It("store ControllerRevision fails if a revision exists with unexpected data", func() {

				unexpectedFlavorSpec := flavorv1alpha1.VirtualMachineFlavorSpec{
					CPU: flavorv1alpha1.CPUFlavor{
						Guest: 15,
					},
				}

				flavorControllerRevision, err := flavor.CreateFlavorControllerRevision(vm, flavor.GetRevisionName(vm.Name, clusterFlavor.Name, clusterFlavor.UID, clusterFlavor.Generation), clusterFlavor.TypeMeta.APIVersion, &unexpectedFlavorSpec)
				Expect(err).ToNot(HaveOccurred())

				_, err = virtClient.AppsV1().ControllerRevisions(vm.Namespace).Create(context.Background(), flavorControllerRevision, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(MatchError(ContainSubstring("found existing ControllerRevision with unexpected data")))
			})

		})

		Context("Using namespaced Flavor", func() {
			var f *flavorv1alpha1.VirtualMachineFlavor
			var fakeFlavorClient v1alpha1.VirtualMachineFlavorInterface

			BeforeEach(func() {

				fakeFlavorClient = fakeFlavorClients.VirtualMachineFlavors(vm.Namespace)
				virtClient.EXPECT().VirtualMachineFlavor(gomock.Any()).Return(fakeFlavorClient).AnyTimes()

				f = &flavorv1alpha1.VirtualMachineFlavor{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "test-flavor",
						Namespace:  vm.Namespace,
						UID:        resourceUID,
						Generation: resourceGeneration,
					},
					Spec: flavorv1alpha1.VirtualMachineFlavorSpec{
						CPU: flavorv1alpha1.CPUFlavor{
							Guest: uint32(2),
						},
					},
				}

				_, err := virtClient.VirtualMachineFlavor(vm.Namespace).Create(context.Background(), f, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				vm.Spec.Flavor = &v1.FlavorMatcher{
					Name: f.Name,
					Kind: apiflavor.SingularResourceName,
				}
			})

			It("find returns expected flavor", func() {
				flavorSpec, err := flavorMethods.FindFlavorSpec(vm)
				Expect(err).ToNot(HaveOccurred())
				Expect(*flavorSpec).To(Equal(f.Spec))
			})

			It("find fails when flavor does not exist", func() {
				vm.Spec.Flavor.Name = "non-existing-flavor"
				_, err := flavorMethods.FindFlavorSpec(vm)
				Expect(err).To(HaveOccurred())
				Expect(errors.IsNotFound(err)).To(BeTrue())
			})

			It("store VirtualMachineFlavor ControllerRevision", func() {

				flavorControllerRevision, err := flavor.CreateFlavorControllerRevision(vm, flavor.GetRevisionName(vm.Name, f.Name, f.UID, f.Generation), f.TypeMeta.APIVersion, &f.Spec)
				Expect(err).ToNot(HaveOccurred())

				expectedRevisionNamePatch, err := flavor.GenerateRevisionNamePatch(flavorControllerRevision, nil)
				Expect(err).ToNot(HaveOccurred())

				vmInterface.EXPECT().Patch(vm.Name, types.JSONPatchType, expectedRevisionNamePatch, &metav1.PatchOptions{})

				expectControllerRevisionCreation(flavorControllerRevision)

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(Succeed())
				Expect(vm.Spec.Flavor.RevisionName).To(Equal(flavorControllerRevision.Name))
			})

			It("store fails when flavor does not exist", func() {
				vm.Spec.Flavor.Name = "non-existing-flavor"

				err := flavorMethods.StoreControllerRevisions(vm)
				Expect(err).To(HaveOccurred())
				Expect(errors.IsNotFound(err)).To(BeTrue())
			})

			It("store returns a nil revision when RevisionName already populated", func() {
				flavorControllerRevision, err := flavor.CreateFlavorControllerRevision(vm, flavor.GetRevisionName(vm.Name, f.Name, f.UID, f.Generation), f.TypeMeta.APIVersion, &f.Spec)
				Expect(err).ToNot(HaveOccurred())

				_, err = virtClient.AppsV1().ControllerRevisions(vm.Namespace).Create(context.Background(), flavorControllerRevision, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				vm.Spec.Flavor = &v1.FlavorMatcher{
					Name:         f.Name,
					RevisionName: flavorControllerRevision.Name,
					Kind:         apiflavor.SingularResourceName,
				}

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(Succeed())
				Expect(vm.Spec.Flavor.RevisionName).To(Equal(flavorControllerRevision.Name))
			})

			It("store ControllerRevision succeeds if a revision exists with expected data", func() {

				flavorControllerRevision, err := flavor.CreateFlavorControllerRevision(vm, flavor.GetRevisionName(vm.Name, f.Name, f.UID, f.Generation), f.TypeMeta.APIVersion, &f.Spec)
				Expect(err).ToNot(HaveOccurred())

				_, err = virtClient.AppsV1().ControllerRevisions(vm.Namespace).Create(context.Background(), flavorControllerRevision, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				expectedRevisionNamePatch, err := flavor.GenerateRevisionNamePatch(flavorControllerRevision, nil)
				Expect(err).ToNot(HaveOccurred())

				vmInterface.EXPECT().Patch(vm.Name, types.JSONPatchType, expectedRevisionNamePatch, &metav1.PatchOptions{})

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(Succeed())
				Expect(vm.Spec.Flavor.RevisionName).To(Equal(flavorControllerRevision.Name))

			})

			It("store ControllerRevision fails if a revision exists with unexpected data", func() {

				unexpectedFlavorSpec := flavorv1alpha1.VirtualMachineFlavorSpec{
					CPU: flavorv1alpha1.CPUFlavor{
						Guest: 15,
					},
				}

				flavorControllerRevision, err := flavor.CreateFlavorControllerRevision(vm, flavor.GetRevisionName(vm.Name, f.Name, f.UID, f.Generation), f.TypeMeta.APIVersion, &unexpectedFlavorSpec)
				Expect(err).ToNot(HaveOccurred())

				_, err = virtClient.AppsV1().ControllerRevisions(vm.Namespace).Create(context.Background(), flavorControllerRevision, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(MatchError(ContainSubstring("found existing ControllerRevision with unexpected data")))
			})
		})
	})

	Context("Add flavor name annotations", func() {
		const flavorName = "flavor-name"

		BeforeEach(func() {
			vm = kubecli.NewMinimalVM("testvm")
			vm.Spec.Flavor = &v1.FlavorMatcher{Name: flavorName}
		})

		It("should add flavor name annotation", func() {
			vm.Spec.Flavor.Kind = apiflavor.SingularResourceName

			meta := &metav1.ObjectMeta{}
			flavor.AddFlavorNameAnnotations(vm, meta)

			Expect(meta.Annotations[v1.FlavorAnnotation]).To(Equal(flavorName))
			Expect(meta.Annotations[v1.ClusterFlavorAnnotation]).To(Equal(""))
		})

		It("should add cluster flavor name annotation", func() {
			vm.Spec.Flavor.Kind = apiflavor.ClusterSingularResourceName

			meta := &metav1.ObjectMeta{}
			flavor.AddFlavorNameAnnotations(vm, meta)

			Expect(meta.Annotations[v1.FlavorAnnotation]).To(Equal(""))
			Expect(meta.Annotations[v1.ClusterFlavorAnnotation]).To(Equal(flavorName))
		})

		It("should add cluster name annotation, if flavor.kind is empty", func() {
			vm.Spec.Flavor.Kind = ""

			meta := &metav1.ObjectMeta{}
			flavor.AddFlavorNameAnnotations(vm, meta)

			Expect(meta.Annotations[v1.FlavorAnnotation]).To(Equal(""))
			Expect(meta.Annotations[v1.ClusterFlavorAnnotation]).To(Equal(flavorName))
		})
	})

	Context("Find and store VirtualMachinePreferenceSpec", func() {

		It("find returns nil when no preference is specified", func() {
			vm.Spec.Preference = nil
			preference, err := flavorMethods.FindPreferenceSpec(vm)
			Expect(err).ToNot(HaveOccurred())
			Expect(preference).To(BeNil())
		})

		It("find returns error when invalid Preference Kind is specified", func() {
			vm.Spec.Preference = &v1.PreferenceMatcher{
				Name: "foo",
				Kind: "bar",
			}
			spec, err := flavorMethods.FindPreferenceSpec(vm)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("got unexpected kind in PreferenceMatcher"))
			Expect(spec).To(BeNil())
		})

		It("store returns error when preferenceMatcher kind is invalid", func() {
			vm.Spec.Preference = &v1.PreferenceMatcher{
				Kind: "foobar",
			}
			Expect(flavorMethods.StoreControllerRevisions(vm)).To(MatchError(ContainSubstring("got unexpected kind in PreferenceMatcher")))
		})

		It("store returns nil when no preference is specified", func() {
			vm.Spec.Preference = nil
			Expect(flavorMethods.StoreControllerRevisions(vm)).To(Succeed())
		})

		Context("Using global ClusterPreference", func() {
			var clusterPreference *flavorv1alpha1.VirtualMachineClusterPreference
			var fakeClusterPreferenceClient v1alpha1.VirtualMachineClusterPreferenceInterface

			BeforeEach(func() {

				fakeClusterPreferenceClient = fakeFlavorClients.VirtualMachineClusterPreferences()
				virtClient.EXPECT().VirtualMachineClusterPreference().Return(fakeClusterPreferenceClient).AnyTimes()

				clusterPreference = &flavorv1alpha1.VirtualMachineClusterPreference{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "test-cluster-preference",
						UID:        resourceUID,
						Generation: resourceGeneration,
					},
					Spec: flavorv1alpha1.VirtualMachinePreferenceSpec{
						CPU: &flavorv1alpha1.CPUPreferences{
							PreferredCPUTopology: flavorv1alpha1.PreferCores,
						},
					},
				}

				_, err := virtClient.VirtualMachineClusterPreference().Create(context.Background(), clusterPreference, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				vm.Spec.Preference = &v1.PreferenceMatcher{
					Name: clusterPreference.Name,
					Kind: apiflavor.ClusterSingularPreferenceResourceName,
				}
			})

			It("find returns expected preference spec", func() {
				s, err := flavorMethods.FindPreferenceSpec(vm)
				Expect(err).ToNot(HaveOccurred())
				Expect(*s).To(Equal(clusterPreference.Spec))
			})

			It("find fails when preference does not exist", func() {
				vm.Spec.Preference.Name = "non-existing-preference"
				_, err := flavorMethods.FindPreferenceSpec(vm)
				Expect(err).To(HaveOccurred())
				Expect(errors.IsNotFound(err)).To(BeTrue())
			})

			It("store VirtualMachineClusterPreference ControllerRevision", func() {

				clusterPreferenceControllerRevision, err := flavor.CreatePreferenceControllerRevision(vm, flavor.GetRevisionName(vm.Name, clusterPreference.Name, clusterPreference.UID, clusterPreference.Generation), clusterPreference.TypeMeta.APIVersion, &clusterPreference.Spec)
				Expect(err).ToNot(HaveOccurred())

				expectedRevisionNamePatch, err := flavor.GenerateRevisionNamePatch(nil, clusterPreferenceControllerRevision)
				Expect(err).ToNot(HaveOccurred())

				vmInterface.EXPECT().Patch(vm.Name, types.JSONPatchType, expectedRevisionNamePatch, &metav1.PatchOptions{})

				expectControllerRevisionCreation(clusterPreferenceControllerRevision)

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(Succeed())
				Expect(vm.Spec.Preference.RevisionName).To(Equal(clusterPreferenceControllerRevision.Name))

			})

			It("store fails when VirtualMachineClusterPreference doesn't exist", func() {
				vm.Spec.Preference.Name = "non-existing-preference"

				err := flavorMethods.StoreControllerRevisions(vm)
				Expect(err).To(HaveOccurred())
				Expect(errors.IsNotFound(err)).To(BeTrue())

			})

			It("store returns nil revision when RevisionName already populated", func() {
				clusterPreferenceControllerRevision, err := flavor.CreatePreferenceControllerRevision(vm, flavor.GetRevisionName(vm.Name, clusterPreference.Name, clusterPreference.UID, clusterPreference.Generation), clusterPreference.TypeMeta.APIVersion, &clusterPreference.Spec)
				Expect(err).ToNot(HaveOccurred())

				_, err = virtClient.AppsV1().ControllerRevisions(vm.Namespace).Create(context.Background(), clusterPreferenceControllerRevision, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				vm.Spec.Preference.RevisionName = clusterPreferenceControllerRevision.Name

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(Succeed())
				Expect(vm.Spec.Preference.RevisionName).To(Equal(clusterPreferenceControllerRevision.Name))

			})

			It("store ControllerRevision succeeds if a revision exists with expected data", func() {

				clusterPreferenceControllerRevision, err := flavor.CreatePreferenceControllerRevision(vm, flavor.GetRevisionName(vm.Name, clusterPreference.Name, clusterPreference.UID, clusterPreference.Generation), clusterPreference.TypeMeta.APIVersion, &clusterPreference.Spec)
				Expect(err).ToNot(HaveOccurred())

				_, err = virtClient.AppsV1().ControllerRevisions(vm.Namespace).Create(context.Background(), clusterPreferenceControllerRevision, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				expectedRevisionNamePatch, err := flavor.GenerateRevisionNamePatch(nil, clusterPreferenceControllerRevision)
				Expect(err).ToNot(HaveOccurred())

				vmInterface.EXPECT().Patch(vm.Name, types.JSONPatchType, expectedRevisionNamePatch, &metav1.PatchOptions{})

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(Succeed())
				Expect(vm.Spec.Preference.RevisionName).To(Equal(clusterPreferenceControllerRevision.Name))

			})

			It("store ControllerRevision fails if a revision exists with unexpected data", func() {

				unexpectedPreferenceSpec := flavorv1alpha1.VirtualMachinePreferenceSpec{
					CPU: &flavorv1alpha1.CPUPreferences{
						PreferredCPUTopology: flavorv1alpha1.PreferThreads,
					},
				}

				clusterPreferenceControllerRevision, err := flavor.CreatePreferenceControllerRevision(vm, flavor.GetRevisionName(vm.Name, clusterPreference.Name, clusterPreference.UID, clusterPreference.Generation), clusterPreference.TypeMeta.APIVersion, &unexpectedPreferenceSpec)
				Expect(err).ToNot(HaveOccurred())

				_, err = virtClient.AppsV1().ControllerRevisions(vm.Namespace).Create(context.Background(), clusterPreferenceControllerRevision, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(MatchError(ContainSubstring("found existing ControllerRevision with unexpected data")))
			})
		})

		Context("Using namespaced Preference", func() {
			var preference *flavorv1alpha1.VirtualMachinePreference
			var fakePreferenceClient v1alpha1.VirtualMachinePreferenceInterface

			BeforeEach(func() {

				fakePreferenceClient = fakeFlavorClients.VirtualMachinePreferences(vm.Namespace)
				virtClient.EXPECT().VirtualMachinePreference(gomock.Any()).Return(fakePreferenceClient).AnyTimes()

				preference = &flavorv1alpha1.VirtualMachinePreference{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "test-preference",
						Namespace:  vm.Namespace,
						UID:        resourceUID,
						Generation: resourceGeneration,
					},
					Spec: flavorv1alpha1.VirtualMachinePreferenceSpec{
						CPU: &flavorv1alpha1.CPUPreferences{
							PreferredCPUTopology: flavorv1alpha1.PreferCores,
						},
					},
				}

				_, err := virtClient.VirtualMachinePreference(vm.Namespace).Create(context.Background(), preference, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				vm.Spec.Preference = &v1.PreferenceMatcher{
					Name: preference.Name,
					Kind: apiflavor.SingularPreferenceResourceName,
				}
			})

			It("find returns expected preference spec", func() {
				s, err := flavorMethods.FindPreferenceSpec(vm)
				Expect(err).ToNot(HaveOccurred())
				Expect(*s).To(Equal(preference.Spec))
			})

			It("find fails when preference does not exist", func() {
				vm.Spec.Preference.Name = "non-existing-preference"
				_, err := flavorMethods.FindPreferenceSpec(vm)
				Expect(err).To(HaveOccurred())
				Expect(errors.IsNotFound(err)).To(BeTrue())
			})

			It("store VirtualMachinePreference ControllerRevision", func() {
				preferenceControllerRevision, err := flavor.CreatePreferenceControllerRevision(vm, flavor.GetRevisionName(vm.Name, preference.Name, preference.UID, preference.Generation), preference.TypeMeta.APIVersion, &preference.Spec)
				Expect(err).ToNot(HaveOccurred())

				expectedRevisionNamePatch, err := flavor.GenerateRevisionNamePatch(nil, preferenceControllerRevision)
				Expect(err).ToNot(HaveOccurred())

				vmInterface.EXPECT().Patch(vm.Name, types.JSONPatchType, expectedRevisionNamePatch, &metav1.PatchOptions{})

				expectControllerRevisionCreation(preferenceControllerRevision)

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(Succeed())
				Expect(vm.Spec.Preference.RevisionName).To(Equal(preferenceControllerRevision.Name))

			})

			It("store fails when VirtualMachinePreference doesn't exist", func() {
				vm.Spec.Preference.Name = "non-existing-preference"

				err := flavorMethods.StoreControllerRevisions(vm)
				Expect(err).To(HaveOccurred())
				Expect(errors.IsNotFound(err)).To(BeTrue())

			})

			It("store returns nil revision when RevisionName already populated", func() {
				preferenceControllerRevision, err := flavor.CreatePreferenceControllerRevision(vm, flavor.GetRevisionName(vm.Name, preference.Name, preference.UID, preference.Generation), preference.TypeMeta.APIVersion, &preference.Spec)
				Expect(err).ToNot(HaveOccurred())

				_, err = virtClient.AppsV1().ControllerRevisions(vm.Namespace).Create(context.Background(), preferenceControllerRevision, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				vm.Spec.Preference.RevisionName = preferenceControllerRevision.Name

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(Succeed())
				Expect(vm.Spec.Preference.RevisionName).To(Equal(preferenceControllerRevision.Name))

			})

			It("store ControllerRevision succeeds if a revision exists with expected data", func() {

				preferenceControllerRevision, err := flavor.CreatePreferenceControllerRevision(vm, flavor.GetRevisionName(vm.Name, preference.Name, preference.UID, preference.Generation), preference.TypeMeta.APIVersion, &preference.Spec)
				Expect(err).ToNot(HaveOccurred())

				_, err = virtClient.AppsV1().ControllerRevisions(vm.Namespace).Create(context.Background(), preferenceControllerRevision, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				expectedRevisionNamePatch, err := flavor.GenerateRevisionNamePatch(nil, preferenceControllerRevision)
				Expect(err).ToNot(HaveOccurred())

				vmInterface.EXPECT().Patch(vm.Name, types.JSONPatchType, expectedRevisionNamePatch, &metav1.PatchOptions{})

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(Succeed())
				Expect(vm.Spec.Preference.RevisionName).To(Equal(preferenceControllerRevision.Name))

			})

			It("store ControllerRevision fails if a revision exists with unexpected data", func() {

				unexpectedPreferenceSpec := flavorv1alpha1.VirtualMachinePreferenceSpec{
					CPU: &flavorv1alpha1.CPUPreferences{
						PreferredCPUTopology: flavorv1alpha1.PreferThreads,
					},
				}

				preferenceControllerRevision, err := flavor.CreatePreferenceControllerRevision(vm, flavor.GetRevisionName(vm.Name, preference.Name, preference.UID, preference.Generation), preference.TypeMeta.APIVersion, &unexpectedPreferenceSpec)
				Expect(err).ToNot(HaveOccurred())

				_, err = virtClient.AppsV1().ControllerRevisions(vm.Namespace).Create(context.Background(), preferenceControllerRevision, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				Expect(flavorMethods.StoreControllerRevisions(vm)).To(MatchError(ContainSubstring("found existing ControllerRevision with unexpected data")))
			})
		})
	})

	Context("Add preference name annotations", func() {
		const preferenceName = "preference-name"

		BeforeEach(func() {
			vm = kubecli.NewMinimalVM("testvm")
			vm.Spec.Preference = &v1.PreferenceMatcher{Name: preferenceName}
		})

		It("should add preference name annotation", func() {
			vm.Spec.Preference.Kind = apiflavor.SingularPreferenceResourceName

			meta := &metav1.ObjectMeta{}
			flavor.AddPreferenceNameAnnotations(vm, meta)

			Expect(meta.Annotations[v1.PreferenceAnnotation]).To(Equal(preferenceName))
			Expect(meta.Annotations[v1.ClusterPreferenceAnnotation]).To(Equal(""))
		})

		It("should add cluster preference name annotation", func() {
			vm.Spec.Preference.Kind = apiflavor.ClusterSingularPreferenceResourceName

			meta := &metav1.ObjectMeta{}
			flavor.AddPreferenceNameAnnotations(vm, meta)

			Expect(meta.Annotations[v1.PreferenceAnnotation]).To(Equal(""))
			Expect(meta.Annotations[v1.ClusterPreferenceAnnotation]).To(Equal(preferenceName))
		})

		It("should add cluster name annotation, if preference.kind is empty", func() {
			vm.Spec.Preference.Kind = ""

			meta := &metav1.ObjectMeta{}
			flavor.AddPreferenceNameAnnotations(vm, meta)

			Expect(meta.Annotations[v1.PreferenceAnnotation]).To(Equal(""))
			Expect(meta.Annotations[v1.ClusterPreferenceAnnotation]).To(Equal(preferenceName))
		})
	})

	Context("Apply", func() {

		var (
			flavorSpec     *flavorv1alpha1.VirtualMachineFlavorSpec
			preferenceSpec *flavorv1alpha1.VirtualMachinePreferenceSpec
			field          *field.Path
		)

		BeforeEach(func() {
			vmi = api.NewMinimalVMI("testvmi")

			vmi.Spec = v1.VirtualMachineInstanceSpec{
				Domain: v1.DomainSpec{},
			}
			field = k8sfield.NewPath("spec", "template", "spec")
		})

		Context("flavor.spec.CPU and preference.spec.CPU", func() {

			BeforeEach(func() {

				flavorSpec = &flavorv1alpha1.VirtualMachineFlavorSpec{
					CPU: flavorv1alpha1.CPUFlavor{
						Guest:                 uint32(2),
						Model:                 "host-passthrough",
						DedicatedCPUPlacement: true,
						IsolateEmulatorThread: true,
						NUMA: &v1.NUMA{
							GuestMappingPassthrough: &v1.NUMAGuestMappingPassthrough{},
						},
						Realtime: &v1.Realtime{
							Mask: "0-3,^1",
						},
					},
				}
				preferenceSpec = &flavorv1alpha1.VirtualMachinePreferenceSpec{
					CPU: &flavorv1alpha1.CPUPreferences{},
				}
			})

			It("should default to PreferSockets", func() {

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(BeEmpty())

				Expect(vmi.Spec.Domain.CPU.Sockets).To(Equal(flavorSpec.CPU.Guest))
				Expect(vmi.Spec.Domain.CPU.Cores).To(Equal(uint32(1)))
				Expect(vmi.Spec.Domain.CPU.Threads).To(Equal(uint32(1)))
				Expect(vmi.Spec.Domain.CPU.Model).To(Equal(flavorSpec.CPU.Model))
				Expect(vmi.Spec.Domain.CPU.DedicatedCPUPlacement).To(Equal(flavorSpec.CPU.DedicatedCPUPlacement))
				Expect(vmi.Spec.Domain.CPU.IsolateEmulatorThread).To(Equal(flavorSpec.CPU.IsolateEmulatorThread))
				Expect(*vmi.Spec.Domain.CPU.NUMA).To(Equal(*flavorSpec.CPU.NUMA))
				Expect(*vmi.Spec.Domain.CPU.Realtime).To(Equal(*flavorSpec.CPU.Realtime))

			})

			It("should apply in full with PreferCores selected", func() {

				preferenceSpec.CPU.PreferredCPUTopology = flavorv1alpha1.PreferCores

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(BeEmpty())

				Expect(vmi.Spec.Domain.CPU.Sockets).To(Equal(uint32(1)))
				Expect(vmi.Spec.Domain.CPU.Cores).To(Equal(flavorSpec.CPU.Guest))
				Expect(vmi.Spec.Domain.CPU.Threads).To(Equal(uint32(1)))
				Expect(vmi.Spec.Domain.CPU.Model).To(Equal(flavorSpec.CPU.Model))
				Expect(vmi.Spec.Domain.CPU.DedicatedCPUPlacement).To(Equal(flavorSpec.CPU.DedicatedCPUPlacement))
				Expect(vmi.Spec.Domain.CPU.IsolateEmulatorThread).To(Equal(flavorSpec.CPU.IsolateEmulatorThread))
				Expect(*vmi.Spec.Domain.CPU.NUMA).To(Equal(*flavorSpec.CPU.NUMA))
				Expect(*vmi.Spec.Domain.CPU.Realtime).To(Equal(*flavorSpec.CPU.Realtime))

			})

			It("should apply in full with PreferThreads selected", func() {

				preferenceSpec.CPU.PreferredCPUTopology = flavorv1alpha1.PreferThreads

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(BeEmpty())

				Expect(vmi.Spec.Domain.CPU.Sockets).To(Equal(uint32(1)))
				Expect(vmi.Spec.Domain.CPU.Cores).To(Equal(uint32(1)))
				Expect(vmi.Spec.Domain.CPU.Threads).To(Equal(flavorSpec.CPU.Guest))
				Expect(vmi.Spec.Domain.CPU.Model).To(Equal(flavorSpec.CPU.Model))
				Expect(vmi.Spec.Domain.CPU.DedicatedCPUPlacement).To(Equal(flavorSpec.CPU.DedicatedCPUPlacement))
				Expect(vmi.Spec.Domain.CPU.IsolateEmulatorThread).To(Equal(flavorSpec.CPU.IsolateEmulatorThread))
				Expect(*vmi.Spec.Domain.CPU.NUMA).To(Equal(*flavorSpec.CPU.NUMA))
				Expect(*vmi.Spec.Domain.CPU.Realtime).To(Equal(*flavorSpec.CPU.Realtime))

			})

			It("should apply in full with PreferSockets selected", func() {

				preferenceSpec.CPU.PreferredCPUTopology = flavorv1alpha1.PreferSockets

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(BeEmpty())

				Expect(vmi.Spec.Domain.CPU.Sockets).To(Equal(flavorSpec.CPU.Guest))
				Expect(vmi.Spec.Domain.CPU.Cores).To(Equal(uint32(1)))
				Expect(vmi.Spec.Domain.CPU.Threads).To(Equal(uint32(1)))
				Expect(vmi.Spec.Domain.CPU.Model).To(Equal(flavorSpec.CPU.Model))
				Expect(vmi.Spec.Domain.CPU.DedicatedCPUPlacement).To(Equal(flavorSpec.CPU.DedicatedCPUPlacement))
				Expect(vmi.Spec.Domain.CPU.IsolateEmulatorThread).To(Equal(flavorSpec.CPU.IsolateEmulatorThread))
				Expect(*vmi.Spec.Domain.CPU.NUMA).To(Equal(*flavorSpec.CPU.NUMA))
				Expect(*vmi.Spec.Domain.CPU.Realtime).To(Equal(*flavorSpec.CPU.Realtime))

			})

			It("should return a conflict if vmi.Spec.Domain.CPU already defined", func() {

				flavorSpec = &flavorv1alpha1.VirtualMachineFlavorSpec{
					CPU: flavorv1alpha1.CPUFlavor{
						Guest: uint32(2),
					},
				}

				vmi.Spec.Domain.CPU = &v1.CPU{
					Cores:   4,
					Sockets: 1,
					Threads: 1,
				}

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(1))
				Expect(conflicts[0].String()).To(Equal("spec.template.spec.domain.cpu"))

			})

			It("should return a conflict if vmi.Spec.Domain.Resources.Requests[k8sv1.ResourceCPU] already defined", func() {

				flavorSpec = &flavorv1alpha1.VirtualMachineFlavorSpec{
					CPU: flavorv1alpha1.CPUFlavor{
						Guest: uint32(2),
					},
				}

				vmi.Spec.Domain.Resources = v1.ResourceRequirements{
					Requests: k8sv1.ResourceList{
						k8sv1.ResourceCPU: resource.MustParse("1"),
					},
				}

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(1))
				Expect(conflicts[0].String()).To(Equal("spec.template.spec.domain.resources.requests.cpu"))

			})

			It("should return a conflict if vmi.Spec.Domain.Resources.Limits[k8sv1.ResourceCPU] already defined", func() {

				flavorSpec = &flavorv1alpha1.VirtualMachineFlavorSpec{
					CPU: flavorv1alpha1.CPUFlavor{
						Guest: uint32(2),
					},
				}

				vmi.Spec.Domain.Resources = v1.ResourceRequirements{
					Limits: k8sv1.ResourceList{
						k8sv1.ResourceCPU: resource.MustParse("1"),
					},
				}

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(1))
				Expect(conflicts[0].String()).To(Equal("spec.template.spec.domain.resources.limits.cpu"))

			})
		})
		Context("flavor.Spec.Memory", func() {
			BeforeEach(func() {
				flavorMem := resource.MustParse("512M")
				flavorSpec = &flavorv1alpha1.VirtualMachineFlavorSpec{
					Memory: flavorv1alpha1.MemoryFlavor{
						Guest: &flavorMem,
						Hugepages: &v1.Hugepages{
							PageSize: "1Gi",
						},
					},
				}
			})

			It("should apply to VMI", func() {

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(BeEmpty())

				Expect(*vmi.Spec.Domain.Memory.Guest).To(Equal(*flavorSpec.Memory.Guest))
				Expect(*vmi.Spec.Domain.Memory.Hugepages).To(Equal(*flavorSpec.Memory.Hugepages))

			})

			It("should detect memory conflict", func() {

				vmiMemGuest := resource.MustParse("512M")
				vmi.Spec.Domain.Memory = &v1.Memory{
					Guest: &vmiMemGuest,
				}

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(1))
				Expect(conflicts[0].String()).To(Equal("spec.template.spec.domain.memory"))

			})

			It("should return a conflict if vmi.Spec.Domain.Resources.Requests[k8sv1.ResourceMemory] already defined", func() {

				vmiMemGuest := resource.MustParse("512M")
				flavorSpec = &flavorv1alpha1.VirtualMachineFlavorSpec{
					Memory: flavorv1alpha1.MemoryFlavor{
						Guest: &vmiMemGuest,
					},
				}

				vmi.Spec.Domain.Resources = v1.ResourceRequirements{
					Requests: k8sv1.ResourceList{
						k8sv1.ResourceMemory: resource.MustParse("128Mi"),
					},
				}

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(1))
				Expect(conflicts[0].String()).To(Equal("spec.template.spec.domain.resources.requests.memory"))

			})

			It("should return a conflict if vmi.Spec.Domain.Resources.Limits[k8sv1.ResourceMemory] already defined", func() {

				vmiMemGuest := resource.MustParse("512M")
				flavorSpec = &flavorv1alpha1.VirtualMachineFlavorSpec{
					Memory: flavorv1alpha1.MemoryFlavor{
						Guest: &vmiMemGuest,
					},
				}

				vmi.Spec.Domain.Resources = v1.ResourceRequirements{
					Limits: k8sv1.ResourceList{
						k8sv1.ResourceMemory: resource.MustParse("128Mi"),
					},
				}

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(1))
				Expect(conflicts[0].String()).To(Equal("spec.template.spec.domain.resources.limits.memory"))

			})
		})
		Context("flavor.Spec.ioThreadsPolicy", func() {

			var flavorPolicy v1.IOThreadsPolicy

			BeforeEach(func() {
				flavorPolicy = v1.IOThreadsPolicyShared
				flavorSpec = &flavorv1alpha1.VirtualMachineFlavorSpec{
					IOThreadsPolicy: &flavorPolicy,
				}
			})

			It("should apply to VMI", func() {
				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(0))

				Expect(*vmi.Spec.Domain.IOThreadsPolicy).To(Equal(*flavorSpec.IOThreadsPolicy))
			})

			It("should detect IOThreadsPolicy conflict", func() {
				vmi.Spec.Domain.IOThreadsPolicy = &flavorPolicy

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(1))
				Expect(conflicts[0].String()).To(Equal("spec.template.spec.domain.ioThreadsPolicy"))
			})
		})

		Context("flavor.Spec.LaunchSecurity", func() {

			BeforeEach(func() {
				flavorSpec = &flavorv1alpha1.VirtualMachineFlavorSpec{
					LaunchSecurity: &v1.LaunchSecurity{
						SEV: &v1.SEV{},
					},
				}
			})

			It("should apply to VMI", func() {
				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(0))

				Expect(*vmi.Spec.Domain.LaunchSecurity).To(Equal(*flavorSpec.LaunchSecurity))
			})

			It("should detect LaunchSecurity conflict", func() {
				vmi.Spec.Domain.LaunchSecurity = &v1.LaunchSecurity{
					SEV: &v1.SEV{},
				}

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(1))
				Expect(conflicts[0].String()).To(Equal("spec.template.spec.domain.launchSecurity"))
			})
		})

		Context("flavor.Spec.GPUs", func() {

			BeforeEach(func() {
				flavorSpec = &flavorv1alpha1.VirtualMachineFlavorSpec{
					GPUs: []v1.GPU{
						v1.GPU{
							Name:       "barfoo",
							DeviceName: "vendor.com/gpu_name",
						},
					},
				}
			})

			It("should apply to VMI", func() {

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(0))

				Expect(vmi.Spec.Domain.Devices.GPUs).To(Equal(flavorSpec.GPUs))

			})

			It("should detect GPU conflict", func() {

				vmi.Spec.Domain.Devices.GPUs = []v1.GPU{
					v1.GPU{
						Name:       "foobar",
						DeviceName: "vendor.com/gpu_name",
					},
				}

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(1))
				Expect(conflicts[0].String()).To(Equal("spec.template.spec.domain.devices.gpus"))

			})
		})

		Context("flavor.Spec.HostDevices", func() {

			BeforeEach(func() {
				flavorSpec = &flavorv1alpha1.VirtualMachineFlavorSpec{
					HostDevices: []v1.HostDevice{
						v1.HostDevice{
							Name:       "foobar",
							DeviceName: "vendor.com/device_name",
						},
					},
				}
			})

			It("should apply to VMI", func() {

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(0))

				Expect(vmi.Spec.Domain.Devices.HostDevices).To(Equal(flavorSpec.HostDevices))

			})

			It("should detect HostDevice conflict", func() {

				vmi.Spec.Domain.Devices.HostDevices = []v1.HostDevice{
					v1.HostDevice{
						Name:       "foobar",
						DeviceName: "vendor.com/device_name",
					},
				}

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(1))
				Expect(conflicts[0].String()).To(Equal("spec.template.spec.domain.devices.hostDevices"))

			})
		})

		// TODO - break this up into smaller more targeted tests
		Context("Preference.Devices", func() {

			var userDefinedBlockSize *v1.BlockSize

			BeforeEach(func() {

				userDefinedBlockSize = &v1.BlockSize{
					Custom: &v1.CustomBlockSize{
						Logical:  512,
						Physical: 512,
					},
				}
				vmi.Spec.Domain.Devices.AutoattachGraphicsDevice = pointer.Bool(false)
				vmi.Spec.Domain.Devices.AutoattachMemBalloon = pointer.Bool(false)
				vmi.Spec.Domain.Devices.Disks = []v1.Disk{
					v1.Disk{
						Cache:             v1.CacheWriteBack,
						IO:                v1.IODefault,
						DedicatedIOThread: pointer.Bool(false),
						BlockSize:         userDefinedBlockSize,
						DiskDevice: v1.DiskDevice{
							Disk: &v1.DiskTarget{
								Bus: v1.DiskBusSCSI,
							},
						},
					},
					v1.Disk{
						DiskDevice: v1.DiskDevice{
							Disk: &v1.DiskTarget{},
						},
					},
					v1.Disk{
						DiskDevice: v1.DiskDevice{
							CDRom: &v1.CDRomTarget{
								Bus: v1.DiskBusSATA,
							},
						},
					},
					v1.Disk{
						DiskDevice: v1.DiskDevice{
							CDRom: &v1.CDRomTarget{},
						},
					},
					v1.Disk{
						DiskDevice: v1.DiskDevice{
							LUN: &v1.LunTarget{
								Bus: v1.DiskBusSATA,
							},
						},
					},
					v1.Disk{
						DiskDevice: v1.DiskDevice{
							LUN: &v1.LunTarget{},
						},
					},
				}
				vmi.Spec.Domain.Devices.Inputs = []v1.Input{
					v1.Input{
						Bus:  "usb",
						Type: "tablet",
					},
					v1.Input{},
				}
				vmi.Spec.Domain.Devices.Interfaces = []v1.Interface{
					v1.Interface{
						Model: "e1000",
					},
					v1.Interface{},
				}
				vmi.Spec.Domain.Devices.Sound = &v1.SoundDevice{}

				preferenceSpec = &flavorv1alpha1.VirtualMachinePreferenceSpec{
					Devices: &flavorv1alpha1.DevicePreferences{
						PreferredAutoattachGraphicsDevice:   pointer.Bool(true),
						PreferredAutoattachMemBalloon:       pointer.Bool(true),
						PreferredAutoattachPodInterface:     pointer.Bool(true),
						PreferredAutoattachSerialConsole:    pointer.Bool(true),
						PreferredDiskDedicatedIoThread:      pointer.Bool(true),
						PreferredDisableHotplug:             pointer.Bool(true),
						PreferredUseVirtioTransitional:      pointer.Bool(true),
						PreferredNetworkInterfaceMultiQueue: pointer.Bool(true),
						PreferredBlockMultiQueue:            pointer.Bool(true),
						PreferredDiskBlockSize: &v1.BlockSize{
							Custom: &v1.CustomBlockSize{
								Logical:  4096,
								Physical: 4096,
							},
						},
						PreferredDiskCache:      v1.CacheWriteThrough,
						PreferredDiskIO:         v1.IONative,
						PreferredDiskBus:        v1.DiskBusVirtio,
						PreferredCdromBus:       v1.DiskBusSCSI,
						PreferredLunBus:         v1.DiskBusSATA,
						PreferredInputBus:       "virtio",
						PreferredInputType:      "tablet",
						PreferredInterfaceModel: "virtio",
						PreferredSoundModel:     "ac97",
						PreferredRng:            &v1.Rng{},
						PreferredTPM:            &v1.TPMDevice{},
					},
				}

			})

			It("should apply to VMI", func() {

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(0))

				Expect(*vmi.Spec.Domain.Devices.AutoattachGraphicsDevice).To(BeFalse())
				Expect(*vmi.Spec.Domain.Devices.AutoattachMemBalloon).To(BeFalse())
				Expect(vmi.Spec.Domain.Devices.Disks[0].Cache).To(Equal(v1.CacheWriteBack))
				Expect(vmi.Spec.Domain.Devices.Disks[0].IO).To(Equal(v1.IODefault))
				Expect(*vmi.Spec.Domain.Devices.Disks[0].DedicatedIOThread).To(BeFalse())
				Expect(*vmi.Spec.Domain.Devices.Disks[0].BlockSize).To(Equal(*userDefinedBlockSize))
				Expect(vmi.Spec.Domain.Devices.Disks[0].DiskDevice.Disk.Bus).To(Equal(v1.DiskBusSCSI))
				Expect(vmi.Spec.Domain.Devices.Disks[2].DiskDevice.CDRom.Bus).To(Equal(v1.DiskBusSATA))
				Expect(vmi.Spec.Domain.Devices.Disks[4].DiskDevice.LUN.Bus).To(Equal(v1.DiskBusSATA))
				Expect(vmi.Spec.Domain.Devices.Inputs[0].Bus).To(Equal("usb"))
				Expect(vmi.Spec.Domain.Devices.Inputs[0].Type).To(Equal("tablet"))
				Expect(vmi.Spec.Domain.Devices.Interfaces[0].Model).To(Equal("e1000"))

				// Assert that everything that isn't defined in the VM/VMI should use Preferences
				Expect(*vmi.Spec.Domain.Devices.AutoattachPodInterface).To(Equal(*preferenceSpec.Devices.PreferredAutoattachPodInterface))
				Expect(*vmi.Spec.Domain.Devices.AutoattachSerialConsole).To(Equal(*preferenceSpec.Devices.PreferredAutoattachSerialConsole))
				Expect(vmi.Spec.Domain.Devices.DisableHotplug).To(Equal(*preferenceSpec.Devices.PreferredDisableHotplug))
				Expect(*vmi.Spec.Domain.Devices.UseVirtioTransitional).To(Equal(*preferenceSpec.Devices.PreferredUseVirtioTransitional))
				Expect(vmi.Spec.Domain.Devices.Disks[1].Cache).To(Equal(preferenceSpec.Devices.PreferredDiskCache))
				Expect(vmi.Spec.Domain.Devices.Disks[1].IO).To(Equal(preferenceSpec.Devices.PreferredDiskIO))
				Expect(*vmi.Spec.Domain.Devices.Disks[1].DedicatedIOThread).To(Equal(*preferenceSpec.Devices.PreferredDiskDedicatedIoThread))
				Expect(*vmi.Spec.Domain.Devices.Disks[1].BlockSize).To(Equal(*preferenceSpec.Devices.PreferredDiskBlockSize))
				Expect(vmi.Spec.Domain.Devices.Disks[1].DiskDevice.Disk.Bus).To(Equal(preferenceSpec.Devices.PreferredDiskBus))
				Expect(vmi.Spec.Domain.Devices.Disks[3].DiskDevice.CDRom.Bus).To(Equal(preferenceSpec.Devices.PreferredCdromBus))
				Expect(vmi.Spec.Domain.Devices.Disks[5].DiskDevice.LUN.Bus).To(Equal(preferenceSpec.Devices.PreferredLunBus))
				Expect(vmi.Spec.Domain.Devices.Inputs[1].Bus).To(Equal(preferenceSpec.Devices.PreferredInputBus))
				Expect(vmi.Spec.Domain.Devices.Inputs[1].Type).To(Equal(preferenceSpec.Devices.PreferredInputType))
				Expect(vmi.Spec.Domain.Devices.Interfaces[1].Model).To(Equal(preferenceSpec.Devices.PreferredInterfaceModel))
				Expect(vmi.Spec.Domain.Devices.Sound.Model).To(Equal(preferenceSpec.Devices.PreferredSoundModel))
				Expect(*vmi.Spec.Domain.Devices.Rng).To(Equal(*preferenceSpec.Devices.PreferredRng))
				Expect(*vmi.Spec.Domain.Devices.NetworkInterfaceMultiQueue).To(Equal(*preferenceSpec.Devices.PreferredNetworkInterfaceMultiQueue))
				Expect(*vmi.Spec.Domain.Devices.BlockMultiQueue).To(Equal(*preferenceSpec.Devices.PreferredBlockMultiQueue))
				Expect(*vmi.Spec.Domain.Devices.TPM).To(Equal(*preferenceSpec.Devices.PreferredTPM))

			})

			It("Should apply when a VMI disk doesn't have a DiskDevice target defined", func() {

				vmi.Spec.Domain.Devices.Disks[1].DiskDevice.Disk = nil

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(0))

				Expect(vmi.Spec.Domain.Devices.Disks[1].DiskDevice.Disk.Bus).To(Equal(preferenceSpec.Devices.PreferredDiskBus))

			})
		})

		Context("Preference.Features", func() {

			BeforeEach(func() {
				spinLockRetries := uint32(32)
				preferenceSpec = &flavorv1alpha1.VirtualMachinePreferenceSpec{
					Features: &flavorv1alpha1.FeaturePreferences{
						PreferredAcpi: &v1.FeatureState{},
						PreferredApic: &v1.FeatureAPIC{
							Enabled:        pointer.Bool(true),
							EndOfInterrupt: false,
						},
						PreferredHyperv: &v1.FeatureHyperv{
							Relaxed: &v1.FeatureState{},
							VAPIC:   &v1.FeatureState{},
							Spinlocks: &v1.FeatureSpinlocks{
								Enabled: pointer.Bool(true),
								Retries: &spinLockRetries,
							},
							VPIndex: &v1.FeatureState{},
							Runtime: &v1.FeatureState{},
							SyNIC:   &v1.FeatureState{},
							SyNICTimer: &v1.SyNICTimer{
								Enabled: pointer.Bool(true),
								Direct:  &v1.FeatureState{},
							},
							Reset: &v1.FeatureState{},
							VendorID: &v1.FeatureVendorID{
								Enabled:  pointer.Bool(true),
								VendorID: "1234",
							},
							Frequencies:     &v1.FeatureState{},
							Reenlightenment: &v1.FeatureState{},
							TLBFlush:        &v1.FeatureState{},
							IPI:             &v1.FeatureState{},
							EVMCS:           &v1.FeatureState{},
						},
						PreferredIoapic: &v1.FeatureIOAPIC{
							Driver: "kvm",
						},
						PreferredKvm: &v1.FeatureKVM{
							Hidden: true,
						},
						PreferredPic:        &v1.FeatureState{},
						PreferredPvspinlock: &v1.FeatureState{},
						PreferredSmm:        &v1.FeatureState{},
					},
				}
			})

			It("should apply to VMI", func() {
				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(0))

				Expect(vmi.Spec.Domain.Features.ACPI).To(Equal(*preferenceSpec.Features.PreferredAcpi))
				Expect(*vmi.Spec.Domain.Features.APIC).To(Equal(*preferenceSpec.Features.PreferredApic))
				Expect(*vmi.Spec.Domain.Features.Hyperv).To(Equal(*preferenceSpec.Features.PreferredHyperv))
				Expect(*vmi.Spec.Domain.Features.IOAPIC).To(Equal(*preferenceSpec.Features.PreferredIoapic))
				Expect(*vmi.Spec.Domain.Features.KVM).To(Equal(*preferenceSpec.Features.PreferredKvm))
				Expect(*vmi.Spec.Domain.Features.PIC).To(Equal(*preferenceSpec.Features.PreferredPic))
				Expect(*vmi.Spec.Domain.Features.Pvspinlock).To(Equal(*preferenceSpec.Features.PreferredPvspinlock))
				Expect(*vmi.Spec.Domain.Features.SMM).To(Equal(*preferenceSpec.Features.PreferredSmm))
			})

			It("should apply when some HyperV features already defined in the VMI", func() {

				vmi.Spec.Domain.Features = &v1.Features{
					Hyperv: &v1.FeatureHyperv{
						EVMCS: &v1.FeatureState{
							Enabled: pointer.Bool(false),
						},
					},
				}

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(0))

				Expect(*vmi.Spec.Domain.Features.Hyperv.EVMCS.Enabled).To(BeFalse())

			})
		})

		Context("Preference.Firmware", func() {

			It("should apply BIOS preferences full to VMI", func() {
				preferenceSpec = &flavorv1alpha1.VirtualMachinePreferenceSpec{
					Firmware: &flavorv1alpha1.FirmwarePreferences{
						PreferredUseBios:       pointer.Bool(true),
						PreferredUseBiosSerial: pointer.Bool(true),
						PreferredUseEfi:        pointer.Bool(false),
						PreferredUseSecureBoot: pointer.Bool(false),
					},
				}

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(0))

				Expect(*vmi.Spec.Domain.Firmware.Bootloader.BIOS.UseSerial).To(Equal(*preferenceSpec.Firmware.PreferredUseBiosSerial))
			})

			It("should apply SecureBoot preferences full to VMI", func() {
				preferenceSpec = &flavorv1alpha1.VirtualMachinePreferenceSpec{
					Firmware: &flavorv1alpha1.FirmwarePreferences{
						PreferredUseBios:       pointer.Bool(false),
						PreferredUseBiosSerial: pointer.Bool(false),
						PreferredUseEfi:        pointer.Bool(true),
						PreferredUseSecureBoot: pointer.Bool(true),
					},
				}

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(0))

				Expect(*vmi.Spec.Domain.Firmware.Bootloader.EFI.SecureBoot).To(Equal(*preferenceSpec.Firmware.PreferredUseSecureBoot))
			})
		})

		Context("Preference.Machine", func() {

			It("should apply to VMI", func() {
				preferenceSpec = &flavorv1alpha1.VirtualMachinePreferenceSpec{
					Machine: &flavorv1alpha1.MachinePreferences{
						PreferredMachineType: "q35-rhel-8.0",
					},
				}

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(0))

				Expect(vmi.Spec.Domain.Machine.Type).To(Equal(preferenceSpec.Machine.PreferredMachineType))
			})
		})
		Context("Preference.Clock", func() {

			It("should apply to VMI", func() {
				preferenceSpec = &flavorv1alpha1.VirtualMachinePreferenceSpec{
					Clock: &flavorv1alpha1.ClockPreferences{
						PreferredClockOffset: &v1.ClockOffset{
							UTC: &v1.ClockOffsetUTC{
								OffsetSeconds: pointer.Int(30),
							},
						},
						PreferredTimer: &v1.Timer{
							Hyperv: &v1.HypervTimer{},
						},
					},
				}

				conflicts := flavorMethods.ApplyToVmi(field, flavorSpec, preferenceSpec, &vmi.Spec)
				Expect(conflicts).To(HaveLen(0))

				Expect(*&vmi.Spec.Domain.Clock.ClockOffset).To(Equal(*preferenceSpec.Clock.PreferredClockOffset))
				Expect(*vmi.Spec.Domain.Clock.Timer).To(Equal(*preferenceSpec.Clock.PreferredTimer))
			})
		})
	})
})
