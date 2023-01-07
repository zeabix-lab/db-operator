/*
Copyright 2023.

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

package controllers

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbv1alpha1 "github.com/zeabix-lab/db-operator/api/v1alpha1"
)

var (
	DatabaseStatusPending = dbv1alpha1.DatabaseStatus{
		Status: "pending",
	}

	DatabaseStatusCreated = dbv1alpha1.DatabaseStatus{
		Status: "created",
	}
)

// DatabaseReconciler reconciles a Database object
type DatabaseReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=db.zeabix.com,resources=databases,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=db.zeabix.com,resources=databases/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=db.zeabix.com,resources=databases/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Database object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.0/pkg/reconcile
func (r *DatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	//Get Database spec
	db := &dbv1alpha1.Database{}
	if err := r.Get(ctx, req.NamespacedName, db); err != nil {
		log.Log.Error(err, "Unable to get Database Spec")
		return ctrl.Result{}, err
	}

	// Check if database have been already created
	if db.Status.Status == DatabaseStatusCreated.Status {
		log.Log.Info("Database has been already created, ignore.")
		return ctrl.Result{}, nil
	}

	// Get MySQLServer from mysqlServerRef
	server := &dbv1alpha1.MySQLServer{}
	serverKey := types.NamespacedName{
		Namespace: req.Namespace,
		Name:      db.Spec.MySQLServerRef,
	}
	if err := r.Get(ctx, serverKey, server); err != nil {
		log.Log.Error(err, "Unable to get MySQLServer")
	}

	// Get secret from MySQLServerRef for admin user credentials
	mysqlSecret := &corev1.Secret{}
	mysqlSecretKey := types.NamespacedName{
		Namespace: req.Namespace,
		Name:      server.Spec.SecretRef,
	}
	if err := r.Get(ctx, mysqlSecretKey, mysqlSecret); err != nil {
		log.Log.Error(err, "Unable to get admin credential")
		return ctrl.Result{}, err
	}

	// Gather connection information
	host := server.Spec.Host
	adminUser := mysqlSecret.Data["username"]
	adminPassword := mysqlSecret.Data["password"]

	connectionStr := fmt.Sprintf("%s:%s@tcp(%s)/mysql?allowNativePasswords=true", adminUser, adminPassword, host)

	// Connect to Database server for create database
	mysqldb, err := sql.Open("mysql", connectionStr)
	if err != nil {
		log.Log.Error(err, "Unable to connect to database")
		_ = r.updateStatus(ctx, db, DatabaseStatusPending)
		return ctrl.Result{}, err
	}
	defer mysqldb.Close()

	// Gather information for database to create
	username := db.Spec.Username
	password := generateRandomPassword(12)
	databaseName := sanitizeDBName(db.ObjectMeta.Name)

	// Create user
	// May be need to add some sanitation for 'username' to prevent SQL injection
	_, err = mysqldb.Exec("CREATE USER '" + username + "'@'%' IDENTIFIED BY '" + password + "'")
	if err != nil {
		log.Log.Error(err, "Unable to create user in database")
		return ctrl.Result{}, err
	}

	log.Log.Info("User is created")

	// Create Database
	_, err = mysqldb.Exec("CREATE DATABASE IF NOT EXISTS " + databaseName)
	if err != nil {
		log.Log.Error(err, "Unable to create database")
		return ctrl.Result{}, err
	}

	log.Log.Info("Database is created")

	// Grant permission
	_, err = mysqldb.Exec("GRANT ALL PRIVILEGES ON " + databaseName + ".* TO '" + username + "'@'%' WITH GRANT OPTION")
	if err != nil {
		log.Log.Error(err, "Unable to grant permission")
		return ctrl.Result{}, err
	}
	log.Log.Info("Privileges are granted")

	// Create Secret for db credential
	secretData := map[string][]byte{
		"host":     []byte(host),
		"user":     []byte(username),
		"password": []byte(password),
		"dbname":   []byte(databaseName),
	}

	meta := metav1.ObjectMeta{Name: db.Spec.SecretRef, Namespace: req.Namespace}
	dbcred := corev1.Secret{
		Data:       secretData,
		ObjectMeta: meta,
	}

	if err := r.Create(ctx, &dbcred); err != nil {
		log.Log.Error(err, "Unable to create secret")
		return ctrl.Result{}, err
	}

	_ = r.updateStatus(ctx, db, DatabaseStatusCreated)
	log.Log.Info("Database is created successfully")
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbv1alpha1.Database{}).
		Complete(r)
}

// Simple function to generate random password for a given length
func generateRandomPassword(n int) string {
	var chars = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0987654321")
	str := make([]rune, n)
	for i := range str {
		str[i] = chars[rand.Intn(len(chars))]
	}
	return string(str)
}

func (r *DatabaseReconciler) updateStatus(ctx context.Context, db *dbv1alpha1.Database, status dbv1alpha1.DatabaseStatus) error {
	db.Status = status
	if err := r.Status().Update(ctx, db); err != nil {
		return err
	}
	return nil
}

// Simple function to remove some characters that MySQL would not allow
// For the DB name, e.g. -
func sanitizeDBName(original string) string {
	return strings.Replace(original, "-", "", -1)
}
