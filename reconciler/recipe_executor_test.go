package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	testConfigMapName = "orpheus-operator-recipes-test"
	testNamespace     = "orpheus-test"
	testJobNamespace  = "orpheus-test"
	imageName         = "maikeee32e/euphrosyne-recipes-test:latest"
)

var recipe_1 = Recipe{
	Config: &RecipeConfig{
		Enabled:     false,
		Image:       imageName,
		Entrypoint:  "test-1-recipe",
		Description: "Test 1 Recipe",
		Params: []struct {
			Name  string `yaml:"name"`
			Value string `yaml:"value"`
		}{
			{Name: "data", Value: "dummy"},
		},
	},
}

var recipe_2 = Recipe{
	Config: &RecipeConfig{
		Enabled:     true,
		Image:       imageName,
		Description: "Test 2 Recipe",
		Entrypoint:  "test-2-recipe",
		Params: []struct {
			Name  string `yaml:"name"`
			Value string `yaml:"value"`
		}{
			{Name: "data", Value: "dummy"},
		},
	},
}

var recipe_1_config = fmt.Sprintf(`
test-1-recipe:
  enabled: false
  image: "%s"
  entrypoint: "test-1-recipe"
  description: "Test 1 Recipe"
  params:
  - name: "data"
    value: "dummy"
`, imageName)

var recipe_2_config = fmt.Sprintf(`
test-2-recipe:
  enabled: true
  image: "%s"
  entrypoint: "test-2-recipe"
  description: "Test 2 Recipe"
  params:
  - name: "data"
    value: "dummy"
`, imageName)

var debuggingRecipes = fmt.Sprintf("%s%s", recipe_1_config, recipe_2_config)

var actionsRecipes = fmt.Sprintf("%s%s", recipe_1_config, recipe_2_config)

var configMap = map[string]string{
	"debugging": debuggingRecipes,
	"actions":   actionsRecipes,
}

var alertData = &map[string]interface{}{
	"uuid": "123",
}

var c *gin.Context

func createTestNamespace() {
	testNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNamespace,
		},
	}
	_, err := clientset.CoreV1().Namespaces().Create(
		context.TODO(), testNamespace, metav1.CreateOptions{},
	)
	if err != nil {
		panic(err.Error())
	}
}

func createTestConfigmap(cMap map[string]string) error {
	configMapObj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testConfigMapName,
			Namespace: testNamespace,
		},
		Data: cMap,
	}
	_, err := clientset.CoreV1().ConfigMaps(testNamespace).Create(
		context.TODO(), configMapObj, metav1.CreateOptions{},
	)
	if err != nil {
		return err
	}
	return nil
}

func deleteTestConfigmap() {
	err := clientset.CoreV1().ConfigMaps(testNamespace).Delete(
		context.TODO(), testConfigMapName, metav1.DeleteOptions{},
	)
	if err != nil {
		panic(err)
	}
}

func init() {
	initLogger()

	configMapNamespace = testNamespace
	configMapName = testConfigMapName
	jobNamespace = testJobNamespace
	var err error
	clientset, err = InitialiseKubernetesClient()
	if err != nil {
		panic(err)
	}
	// check whether the test namespace exists
	_, err = clientset.CoreV1().Namespaces().Get(
		context.TODO(), testNamespace, metav1.GetOptions{},
	)
	if err != nil {
		createTestNamespace()
	}

	w := httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)

	// make sure redis is running
	connectRedis(&Config{RedisAddress: "localhost:6379"})
}

// Test that the recipe executor can retrieve recipes from the ConfigMap.
func Test_GetRecipeConfig(t *testing.T) {
	defer deleteTestConfigmap()

	testRecipeMap := map[string]Recipe{
		"test-1-recipe": recipe_1,
		"test-2-recipe": recipe_2,
	}

	err := createTestConfigmap(configMap)
	assert.Nil(t, err)

	for _, requestType := range []RequestType{Actions, Alert} {
		recipe, err := getRecipesFromConfigMap(requestType, false)
		assert.Nil(t, err)
		assert.Equal(t, len(testRecipeMap), len(recipe))

		assert.Equal(t, testRecipeMap["test-1-recipe"], recipe["test-1-recipe"])
		assert.Equal(t, testRecipeMap["test-2-recipe"], recipe["test-2-recipe"])
	}

	// Test that the recipe executor can retrieve only enabled recipes from the ConfigMap.
	for _, requestType := range []RequestType{Actions, Alert} {
		recipe, err := getRecipesFromConfigMap(requestType, true)
		assert.Nil(t, err)
		assert.Equal(t, 1, len(recipe))

		assert.Equal(t, testRecipeMap["test-2-recipe"], recipe["test-2-recipe"])
	}
}

// Test that the recipe executor can create a Job for the provided alert data.
func Test_CreateJob(t *testing.T) {
	testConfig := Config{
		AggregatorAddress: "localhost:8080",
		RedisAddress:      "localhost:6379",
	}
	job, err := createJob("test-1-recipe", recipe_1, alertData, &testConfig)
	assert.NotNil(t, job)
	assert.Nil(t, err)
	getJob, err := clientset.BatchV1().Jobs(testNamespace).Get(
		context.TODO(), job.Name, metav1.GetOptions{},
	)
	assert.NotNil(t, getJob)
	assert.Nil(t, err)
	err = clientset.BatchV1().Jobs(testNamespace).Delete(
		context.TODO(), job.Name, metav1.DeleteOptions{},
	)
	assert.Nil(t, err)
}
