package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Test Reconciler.
func TestReconciler(t *testing.T) {
	// Check whether the test namespace exists
	_, err := clientset.CoreV1().Namespaces().Get(
		context.TODO(), testNamespace, metav1.GetOptions{},
	)
	if err != nil {
		createTestNamespace()
	}
	defer deleteNamespace(testNamespace)

	testCollectRecipeResult(t)

	testCleanup(t)
}

// Test that the reconciler can collect the results of completed recipes from Redis.
func testCollectRecipeResult(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(2)

	testConfig := Config{
		RecipeTimeout:       2,
		RecipeNamespace:     testNamespace,
		ReconcilerNamespace: testNamespace,
	}
	// simulate 2 recipes
	testRecipeMap := map[string]Recipe{
		"test-1-recipe": recipe_1,
		"test-2-recipe": recipe_2,
	}

	recipeMsg1 := `{"name": "test-1-recipe"}`
	recipeMsg2 := `{"name": "test-2-recipe"}`
	var requestType RequestType = Alert
	r, err := NewReconciler(c, &testConfig, alertData, testRecipeMap, requestType)
	assert.NotNil(t, r)
	assert.Nil(t, err)

	// test that successful recipes are detected and their results are collected
	go func() {
		defer wg.Done()
		time.Sleep(time.Second)
		rdb.Publish(c, (*alertData)["uuid"].(string), recipeMsg1)
	}()

	go func() {
		defer wg.Done()
		time.Sleep(time.Second)
		rdb.Publish(c, (*alertData)["uuid"].(string), recipeMsg2)
	}()

	completedRecipes, err := collectRecipeResult(r)

	assert.Nil(t, err)
	assert.Equal(t, 2, len(completedRecipes))
	wg.Wait()

	// test that the reconciler can handle a recipe that times out
	wg.Add(2)
	r, err = NewReconciler(c, &testConfig, alertData, testRecipeMap, requestType)
	assert.NotNil(t, r)
	assert.Nil(t, err)

	go func() {
		defer wg.Done()
		time.Sleep(time.Second)
		rdb.Publish(c, (*alertData)["uuid"].(string), recipeMsg1)
	}()

	go func() {
		defer wg.Done()
		time.Sleep(3 * time.Second)
	}()
	completedRecipes, err = collectRecipeResult(r)
	assert.Nil(t, err)
	assert.Equal(t, 1, len(completedRecipes))
	wg.Wait()
}

// Test that created resources are cleaned up successfully.
func testCleanup(t *testing.T) {
	testConfig := Config{
		RecipeTimeout:       2,
		RecipeNamespace:     testNamespace,
		ReconcilerNamespace: testNamespace,
	}

	// a Job that is expected to run successfully
	jobObj := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "test-job-",
			Labels: map[string]string{
				"app":    "euphrosyne",
				"recipe": "test-job",
				"uuid":   (*alertData)["uuid"].(string),
			},
			Namespace: testNamespace,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":    "euphrosyne",
						"recipe": "test-job",
						"uuid":   (*alertData)["uuid"].(string),
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "test-job",
							Image:   "busybox",
							Command: []string{"echo", "Hello from Kubernetes job"},
						},
					},
					RestartPolicy: corev1.RestartPolicyNever,
				},
			},
		},
	}

	// a ConfigMap that is expected to be deleted
	configMapObj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-configmap",
			Namespace: testNamespace,
			Labels: map[string]string{
				"app":  "euphrosyne",
				"uuid": (*alertData)["uuid"].(string),
			},
		},
		Data: map[string]string{
			"key": "value",
		},
	}

	completedRecipe := Recipe{
		Execution: &struct {
			Name     string "json:\"name\""
			Incident string "json:\"incident\""
			Status   string "json:\"status\""
			Results  struct {
				Actions  []string "json:\"actions\""
				Analysis string   "json:\"analysis\""
				JSON     string   "json:\"json\""
				Links    []string "json:\"links\""
			} "json:\"results\""
		}{Name: "test-job"},
	}
	completedRecipes := []Recipe{
		completedRecipe,
	}

	var requestType RequestType = Alert
	r, err := NewReconciler(c, &testConfig, alertData, nil, requestType)
	assert.Nil(t, err)

	job, err := clientset.BatchV1().Jobs(testNamespace).Create(
		context.TODO(), jobObj, metav1.CreateOptions{},
	)
	assert.NotNil(t, job)
	assert.Nil(t, err)

	configMap, err := clientset.CoreV1().ConfigMaps(testNamespace).Create(
		context.TODO(), configMapObj, metav1.CreateOptions{},
	)
	assert.NotNil(t, configMap)
	assert.Nil(t, err)

	for {
		getJob, err := clientset.BatchV1().Jobs(testNamespace).Get(
			context.TODO(), job.Name, metav1.GetOptions{},
		)
		assert.NotNil(t, getJob)
		assert.Nil(t, err)
		if getJob.Status.Succeeded > 0 {
			r.Cleanup(completedRecipes)
			break
		}
		time.Sleep(1 * time.Second)
	}

	// Set a timeout for waiting
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

JobLoop:
	// Wait until the Job is deleted
	for {
		select {
		case <-ctx.Done():
			t.Fatal("Timeout waiting for Job deletion")
		default:
			getJob, err := clientset.BatchV1().Jobs(testNamespace).Get(
				context.TODO(), job.Name, metav1.GetOptions{},
			)
			if errors.IsNotFound(err) {
				assert.Equal(t, "", getJob.Name)
				break JobLoop
			}
			time.Sleep(1 * time.Second)
		}
	}

ConfigMapLoop:
	// Wait until the ConfigMap is deleted
	for {
		select {
		case <-ctx.Done():
			t.Fatal("Timeout waiting for ConfigMap deletion")
		default:
			getConfigMap, err := clientset.CoreV1().ConfigMaps(testNamespace).Get(
				context.TODO(), configMap.Name, metav1.GetOptions{},
			)
			if errors.IsNotFound(err) {
				assert.Equal(t, "", getConfigMap.Name)
				break ConfigMapLoop
			}
			time.Sleep(1 * time.Second)
		}
	}
}
