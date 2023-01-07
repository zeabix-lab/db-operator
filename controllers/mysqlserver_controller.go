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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbv1alpha1 "github.com/zeabix-lab/db-operator/api/v1alpha1"

	"database/sql"

	_ "github.com/go-sql-driver/mysql"
)

var (
	MySQLServerStatusOK = dbv1alpha1.MySQLServerStatus{
		Health: "ok",
	}

	MySQLServerStatusUnavailable = dbv1alpha1.MySQLServerStatus{
		Health: "unavailable",
	}
)

// MySQLServerReconciler reconciles a MySQLServer object
type MySQLServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=db.zeabix.com,resources=mysqlservers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=db.zeabix.com,resources=mysqlservers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=db.zeabix.com,resources=mysqlservers/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MySQLServer object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.0/pkg/reconcile
func (r *MySQLServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)
	db := &dbv1alpha1.MySQLServer{}
	err := r.Get(ctx, req.NamespacedName, db)
	if err != nil {
		log.Log.Error(err, "Unable to deserialized MySQLServer Object")
		return ctrl.Result{}, err
	}

	// Get information for MySQL credential
	var secret corev1.Secret
	secretRef := types.NamespacedName{
		Namespace: req.Namespace,
		Name:      db.Spec.SecretRef,
	}
	if err := r.Get(ctx, secretRef, &secret); err != nil {
		log.Log.Error(err, "Unable to retrieve secret")
		return ctrl.Result{}, err
	}

	// Gather connection string
	host := db.Spec.Host
	username := string(secret.Data["username"])
	password := string(secret.Data["password"])
	connectionStr := fmt.Sprintf("%s:%s@tcp(%s)/mysql?allowNativePasswords=true", username, password, host)

	// Create mysql connection
	mysqldb, err := sql.Open("mysql", connectionStr)
	if err != nil {
		log.Log.Error(err, "Unable to connect to database")
		_ = r.updateHealth(ctx, db, MySQLServerStatusUnavailable)
		return ctrl.Result{}, err
	}
	defer mysqldb.Close()

	// Test Ping
	err = mysqldb.Ping()
	if err != nil {
		log.Log.Error(err, "Unable to connect to database")
		_ = r.updateHealth(ctx, db, MySQLServerStatusUnavailable)
		return ctrl.Result{}, err
	}

	// No news is good news
	log.Log.Info("Database is good ;)")
	_ = r.updateHealth(ctx, db, MySQLServerStatusOK)

	return ctrl.Result{RequeueAfter: time.Second * 5}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MySQLServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbv1alpha1.MySQLServer{}).
		Complete(r)
}

func (r *MySQLServerReconciler) updateHealth(ctx context.Context, db *dbv1alpha1.MySQLServer, status dbv1alpha1.MySQLServerStatus) error {
	db.Status = status
	if err := r.Status().Update(ctx, db); err != nil {
		return err
	}
	return nil
}
